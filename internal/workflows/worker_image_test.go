package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
)

func TestVolumeBackupPreparesWorkerBeforeStoppingWriters(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprint("local=", local), func(t *testing.T) {
			s := testService(t)
			root := s.app.Config.AllowedPaths[0]
			repo := filepath.Join(root, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			addWorkerTestRepository(t, s, repo)
			s.app.Config.BackupWorkerImage = "test/explicit-worker:local"
			var mutations, inspections, volumes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method != "GET":
					mutations.Add(1)
					http.Error(w, "unexpected mutation", 500)
				case strings.Contains(r.URL.Path, "/volumes/data"):
					volumes.Add(1)
					fmt.Fprintf(w, `{"Name":"data","Driver":"local","Mountpoint":%q}`, filepath.Join(root, "volume"))
				case strings.Contains(r.URL.Path, "/images/"):
					inspections.Add(1)
					if !local {
						http.Error(w, `{"message":"No such image"}`, 404)
						return
					}
					fmt.Fprintf(w, `{"Id":"sha256:%s","Os":"linux","Architecture":%q}`, strings.Repeat("a", 64), runtime.GOARCH)
				case strings.HasSuffix(r.URL.Path, "/containers/json"):
					fmt.Fprintf(w, `[{"Id":%q,"State":"running","Mounts":[{"Type":"volume","Name":"data","RW":true}]}]`, strings.Repeat("b", 64))
				default:
					t.Errorf("unexpected Docker request: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			docker, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			s.app.Docker = docker
			ctx, cancel := context.WithCancel(context.Background())
			if err = s.app.Tasks.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); s.app.Tasks.Wait() }()
			policy := Policy{ID: "policy", RepositoryID: "repo", Targets: []Target{{Type: "volume", Name: "data"}}, Consistency: "stop"}
			job, err := s.app.Tasks.Submit(ctx, "backup", "policy", "test", "", true, func(run *task.Run) error { return s.backup(run, policy) })
			if err != nil {
				t.Fatal(err)
			}
			result := waitTask(t, s.app.Tasks, job.ID)
			want := "prepare backup/restore worker image"
			if local {
				want = "outside the policy's approved stop scope"
			}
			if result.Status != "Failed" || !strings.Contains(result.ErrorMessage, want) || mutations.Load() != 0 || inspections.Load() != 1 {
				t.Fatalf("result=%+v mutations=%d inspections=%d", result, mutations.Load(), inspections.Load())
			}
			if local && volumes.Load() != 2 {
				t.Fatal("backup did not re-read source identity after preparing the worker")
			}
			entries, err := os.ReadDir(repo)
			if err != nil || len(entries) != 0 {
				t.Fatal("worker preparation or rejected writer scope changed repository contents")
			}
		})
	}
}

func TestRestorePreviewDefersWorkerAndFailedPreparationPreservesTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can restore ownership directly without a worker")
	}
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprint("overwrite=", overwrite), func(t *testing.T) {
			s := testService(t)
			root := s.app.Config.AllowedPaths[0]
			repo, target := filepath.Join(root, "repo"), filepath.Join(root, "target")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			if overwrite {
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "payload"), []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			addWorkerTestRepository(t, s, repo)
			s.app.Config.BackupWorkerImage = "test/explicit-worker:absent"
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			s.app.Config.ResticPath = executable
			t.Setenv("WORKFLOW_LAZY_WORKER_RESTIC", "1")
			manifest := Manifest{Version: 1, Sources: []backupSource{{HostPath: target, SnapshotPath: "/sources/000"}}}
			t.Setenv("WORKFLOW_LAZY_WORKER_MANIFEST", jsonString(manifest))
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "GET" || !strings.Contains(r.URL.Path, "/images/") {
					t.Errorf("unexpected mutation or writer access: %s %s", r.Method, r.URL.Path)
				}
				http.Error(w, `{"message":"No such image"}`, 404)
			}))
			defer server.Close()
			docker, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			s.app.Docker = docker
			ctx, cancel := context.WithCancel(context.Background())
			if err = s.app.Tasks.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); s.app.Tasks.Wait() }()
			req := restoreRequest{RepositoryID: "repo", Snapshot: strings.Repeat("a", 64), Target: target, Overwrite: overwrite}
			_, _, _, revision, err := s.restorePreflight(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 0 {
				t.Fatal("preview contacted Docker to prepare an image")
			}
			req.Confirm, req.Revision = true, revision
			if overwrite {
				req.ConfirmOverwrite = target
			}
			router := gin.New()
			router.POST("/restore", s.restore)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("POST", "/restore", strings.NewReader(jsonString(req))))
			var job task.Task
			if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &job) != nil {
				t.Fatal(response.Code, response.Body.String())
			}
			result := waitTask(t, s.app.Tasks, job.ID)
			if result.Status != "Failed" || !strings.Contains(result.ErrorMessage, "prepare backup/restore worker image") || requests.Load() != 1 {
				t.Fatal(result, requests.Load())
			}
			if overwrite {
				content, err := os.ReadFile(filepath.Join(target, "payload"))
				if err != nil || string(content) != "original" {
					t.Fatal("failed preparation modified the original data")
				}
			} else if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("failed preparation created a restore target")
			}
		})
	}
}

func addWorkerTestRepository(t *testing.T, s *Service, repo string) {
	t.Helper()
	sealed, err := s.app.Vault.Seal("test-repository-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.app.DB.Exec(`INSERT INTO backup_repositories VALUES('repo','test',?,?,'ready',?)`, repo, sealed, now()); err != nil {
		t.Fatal(err)
	}
}

func handleLazyWorkerRestic() bool {
	if os.Getenv("WORKFLOW_LAZY_WORKER_RESTIC") != "1" || len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "ls":
		fmt.Println(`{"struct_type":"node","name":"manifest.json","path":"/manifest/manifest.json","type":"file"}`)
		fmt.Printf("{\"struct_type\":\"node\",\"name\":\"data\",\"path\":\"/sources/000\",\"type\":\"dir\",\"uid\":%d,\"gid\":%d,\"mode\":448}\n", os.Geteuid()+1, os.Getegid())
	case "dump":
		fmt.Println(os.Getenv("WORKFLOW_LAZY_WORKER_MANIFEST"))
	case "cat":
		fmt.Println(`{"tags":["dm-verified:v1"]}`)
	default:
		os.Exit(2)
	}
	return true
}
