package workflows

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

func identityTar(t *testing.T, data []byte, extra bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "master.key", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err := writer.WriteHeader(&tar.Header{Name: "unexpected", Typeflag: tar.TypeReg, Mode: 0600}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func identityReply(t *testing.T, writer http.ResponseWriter, body []byte) {
	t.Helper()
	stat, err := json.Marshal(container.PathStat{Name: "master.key", Size: 32, Mode: 0600})
	if err != nil {
		t.Fatal(err)
	}
	writer.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
	writer.Header().Set("Content-Type", "application/x-tar")
	_, _ = writer.Write(body)
}

func identityService(t *testing.T, engine http.HandlerFunc) *Service {
	t.Helper()
	s := testService(t)
	canonical, err := filepath.EvalSymlinks(s.app.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.app.Config.DataDir = canonical
	s.app.Config.Mode = "docker"
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	docker, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	s.app.Docker = docker
	return s
}

func TestStopGuardProtectsCustomHostnameAndNamedVolumeInstallation(t *testing.T) {
	own, writer, missing := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	t.Setenv("HOSTNAME", writer) // A custom/wrong hostname must not identify a different container.
	var key []byte
	var expectedPath string
	var archiveCalls atomic.Int32
	s := identityService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			if r.URL.Query().Get("all") != "1" {
				t.Error("self discovery must not filter out containers by labels or hostname")
			}
			_ = json.NewEncoder(w).Encode([]container.Summary{
				{ID: own, Names: []string{"/custom-manager-name"}, Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "named-installation-data", Destination: "/nonstandard/data", RW: true}}},
				{ID: writer, Names: []string{"/ordinary-volume-writer"}}, {ID: missing},
			})
		case strings.HasSuffix(r.URL.Path, "/archive"):
			archiveCalls.Add(1)
			if r.URL.Query().Get("path") != expectedPath {
				t.Error("self discovery ignored the configured data directory")
			}
			switch {
			case strings.Contains(r.URL.Path, own):
				identityReply(t, w, identityTar(t, key, false))
			case strings.Contains(r.URL.Path, writer):
				identityReply(t, w, identityTar(t, bytes.Repeat([]byte{'x'}, 32), false))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			t.Errorf("self guard must not change Engine resources: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	})
	key, _ = os.ReadFile(filepath.Join(s.app.Config.DataDir, "master.key"))
	expectedPath = filepath.Join(s.app.Config.DataDir, "master.key")
	if err := s.guardStopWriters(context.Background(), []string{own}); err == nil || !strings.Contains(err.Error(), "refusing to stop") || bytes.Contains([]byte(err.Error()), key) {
		t.Fatalf("custom-hostname manager was not protected: %v", err)
	}
	if err := s.guardStopWriters(context.Background(), []string{writer}); err != nil {
		t.Fatalf("identified manager outside writer scope incorrectly blocked a Volume backup: %v", err)
	}
	if archiveCalls.Load() < 4 {
		t.Fatal("positive self discovery did not also inspect unrelated containers and missing paths")
	}
}

func TestStopGuardProtectsEveryContainerSharingInstallationData(t *testing.T) {
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var key []byte
	s := identityService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			_ = json.NewEncoder(w).Encode([]container.Summary{{ID: first}, {ID: second}})
			return
		}
		identityReply(t, w, identityTar(t, key, false))
	})
	key, _ = os.ReadFile(filepath.Join(s.app.Config.DataDir, "master.key"))
	if err := s.guardStopWriters(context.Background(), []string{second}); err == nil {
		t.Fatal("first matching clone hid a second protected writer")
	}
}

func TestStopGuardFailsClosedOnUnverifiableIdentity(t *testing.T) {
	for _, scenario := range []string{"missing", "different-key", "copy-error", "oversized-stream", "extra-entry", "symlink", "truncated", "invalid-local-key"} {
		t.Run(scenario, func(t *testing.T) {
			own := strings.Repeat("a", 64)
			var key []byte
			s := identityService(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/containers/json") {
					_ = json.NewEncoder(w).Encode([]container.Summary{{ID: own}})
					return
				}
				if scenario == "missing" {
					w.WriteHeader(404)
					return
				}
				if scenario == "copy-error" {
					w.WriteHeader(500)
					_, _ = w.Write(key) // Untrusted error content must never be returned or logged.
					return
				}
				body := identityTar(t, key, scenario == "extra-entry")
				switch scenario {
				case "different-key":
					body = identityTar(t, bytes.Repeat([]byte{'x'}, 32), false)
				case "oversized-stream":
					body = append(body, make([]byte, selfIdentityArchiveLimit+1)...)
				case "truncated":
					body = body[:520]
				case "symlink":
					var b bytes.Buffer
					archive := tar.NewWriter(&b)
					_ = archive.WriteHeader(&tar.Header{Name: "master.key", Typeflag: tar.TypeSymlink, Linkname: "/outside/key"})
					_ = archive.Close()
					body = b.Bytes()
				}
				identityReply(t, w, body)
			})
			key, _ = os.ReadFile(filepath.Join(s.app.Config.DataDir, "master.key"))
			if scenario == "invalid-local-key" {
				if err := os.WriteFile(filepath.Join(s.app.Config.DataDir, "master.key"), []byte("short"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := s.guardStopWriters(context.Background(), []string{own})
			if err == nil || !strings.Contains(err.Error(), "read-only") || bytes.Contains([]byte(err.Error()), key) {
				t.Fatalf("unverifiable manager was not safely rejected: %v", err)
			}
		})
	}
}

func TestIdentityArchiveBoundsAndMetadata(t *testing.T) {
	key := bytes.Repeat([]byte{'k'}, 32)
	expected := sha256.Sum256(key)
	for _, scenario := range []string{"valid", "wrong-size", "extra", "garbage-trailer", "empty", "invalid-tar"} {
		t.Run(scenario, func(t *testing.T) {
			body := identityTar(t, key, scenario == "extra")
			switch scenario {
			case "wrong-size":
				body = identityTar(t, append(key, 'x'), false)
			case "garbage-trailer":
				body = append(body, []byte("untrusted content")...)
			case "empty":
				body = nil
			case "invalid-tar":
				body = []byte("invalid tar")
			}
			matches, err := identityArchiveMatches(body, expected)
			if scenario == "valid" {
				if err != nil || !matches {
					t.Fatalf("valid bounded identity rejected: %v", err)
				}
			} else if err == nil || matches {
				t.Fatalf("untrusted archive accepted: %s", scenario)
			}
		})
	}
}

func TestStopGuardBypassesBinaryAndEmptyDockerScope(t *testing.T) {
	s := testService(t) // No Docker adapter: bypass must perform no Engine request.
	if err := s.guardStopWriters(context.Background(), []string{strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	s.app.Config.Mode = "docker"
	if err := s.guardStopWriters(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSelfStopRejectedBeforeCheckpointOrAnyContainerStop(t *testing.T) {
	own := strings.Repeat("a", 64)
	var key []byte
	var source string
	var stopCalls atomic.Int32
	s := identityService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]container.Summary{{ID: own, State: "running", Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: source, Destination: "/source", RW: true}}}})
		case strings.HasSuffix(r.URL.Path, "/archive"):
			identityReply(t, w, identityTar(t, key, false))
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stopCalls.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected preflight request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	key, _ = os.ReadFile(filepath.Join(s.app.Config.DataDir, "master.key"))
	root := filepath.Dir(s.app.Config.DataDir)
	s.app.Config.PathMappings = []config.PathMapping{{Host: root, Local: root}}
	s.app.Config.AllowedPaths = []string{root}
	source = filepath.Join(root, "source")
	repo := filepath.Join(root, "repository")
	for _, dir := range []string{source, repo} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	secret, err := s.app.Vault.Seal("owned-repository-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.app.DB.Exec(`INSERT INTO backup_repositories(id,name,location,encrypted_secret,status,created_at) VALUES('owned','owned',?,?,'ready',?)`, repo, secret, now())
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{ID: "owned", Name: "owned", RepositoryID: "owned", Targets: []Target{{Type: "directory", Path: source}}, Consistency: "stop", AllowedWriters: []string{own}, KeepLast: 1, Timezone: "UTC"}
	if _, _, _, err = s.backupPreflight(context.Background(), policy); err == nil {
		t.Fatal("preview accepted its own manager in stop scope")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = s.app.Tasks.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	accepted, err := s.app.Tasks.Submit(ctx, "backup", "owned", "test", "", true, func(run *task.Run) error { return s.backup(run, policy) })
	if err != nil {
		t.Fatal(err)
	}
	result := waitTask(t, s.app.Tasks, accepted.ID)
	var checkpoint string
	if err = s.app.DB.QueryRow("SELECT checkpoint FROM tasks WHERE id=?", accepted.ID).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if result.Status != "Failed" || stopCalls.Load() != 0 || checkpoint != "{}" {
		t.Fatalf("self-stop escaped preflight: task=%+v stops=%d checkpoint=%s", result, stopCalls.Load(), checkpoint)
	}
}

func TestStopGuardCancellationPreservesContext(t *testing.T) {
	started := make(chan struct{})
	var once atomic.Bool
	s := identityService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			_, _ = fmt.Fprintf(w, `[{"Id":%q}]`, strings.Repeat("a", 64))
			return
		}
		if once.CompareAndSwap(false, true) {
			close(started)
		}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.guardStopWriters(ctx, []string{strings.Repeat("a", 64)}) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("identity read did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("guard lost cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guard did not release its canceled archive request")
	}
}
