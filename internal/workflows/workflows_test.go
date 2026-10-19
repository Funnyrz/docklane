package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"
)

func testService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	db, e := database.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	vault, e := security.NewVault(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	app := &platform.App{DB: db, Vault: vault, Tasks: task.New(db), Config: config.Config{Mode: "binary", AllowedPaths: []string{dir}, DataDir: filepath.Join(dir, "data"), ResticPath: os.Getenv("DM_TEST_RESTIC")}}
	s := &Service{app: app, running: map[string]bool{}}
	if e = s.schema(); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestComposeArgumentsPreserveOrderingAndTreatInputAsData(t *testing.T) {
	p := Project{Name: "stack", ConfigPaths: []string{"a", "b"}, EnvFiles: []string{"e"}, Profiles: []string{"jobs"}}
	got := composeArgs(p, []string{"/tmp/base.yaml", "/tmp/over ride.yaml", "/tmp/env"}, "config", "--format", "json")
	want := []string{"compose", "--project-name", "stack", "--file", "/tmp/base.yaml", "--file", "/tmp/over ride.yaml", "--env-file", "/tmp/env", "--profile", "jobs", "config", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if validEnvironment("PATH=malicious") || validEnvironment("A;touch x") {
		t.Fatal("invalid environment accepted")
	}
}
func TestAuthorizedMappingsAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "escape")); e != nil {
		t.Fatal(e)
	}
	s := &Service{app: &platform.App{Config: config.Config{Mode: "docker", PathMappings: []config.PathMapping{{Host: "/srv", Local: root}}}}}
	if _, e := s.path("/srv/new/path"); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"/etc/passwd", "/srv/escape/file", "/srv/../etc/passwd", "relative"} {
		if _, e := s.path(p); e == nil {
			t.Fatalf("unauthorized path accepted: %s", p)
		}
	}
}
func TestMirrorMergePreservesUnknownFieldsAndRejectsNonObjects(t *testing.T) {
	original := []byte(`{"log-driver":"local","features":{"buildkit":true},"custom":{"integer":9007199254740993}}`)
	b, e := mergeMirrors(original, []string{"https://mirror.example"})
	if e != nil {
		t.Fatal(e)
	}
	var got map[string]json.RawMessage
	if e = json.Unmarshal(b, &got); e != nil {
		t.Fatal(e)
	}
	if string(got["log-driver"]) != `"local"` || !strings.Contains(string(got["custom"]), "9007199254740993") {
		t.Fatal(string(b))
	}
	for _, input := range []string{"null", "[]", "{invalid"} {
		if _, e = mergeMirrors([]byte(input), nil); e == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, mirror := range []string{"file:///etc/passwd", "https://user:pass@host", "https://host/path", "https://host?q=1"} {
		if _, e = mergeMirrors(nil, []string{mirror}); e == nil {
			t.Fatal(mirror)
		}
	}
}
func TestCronTimezoneAndLogicalBackupConstraints(t *testing.T) {
	p := Policy{Name: "data", RepositoryID: "r", Targets: []Target{{Type: "directory", Path: "/srv/data"}}, Consistency: "stop", KeepLast: 7, Timezone: "America/New_York", Schedule: "30 1 * * *"}
	if e := validatePolicy(p); e != nil {
		t.Fatal(e)
	}
	p.Timezone = "invalid/zone"
	if validatePolicy(p) == nil {
		t.Fatal("invalid timezone accepted")
	}
	p.Timezone = "Asia/Shanghai"
	p.Consistency = "logical"
	if validatePolicy(p) == nil {
		t.Fatal("unsupported logical mode accepted")
	}
}
func TestRestoreRevisionIgnoresConfirmationButIncludesSelection(t *testing.T) {
	r := restoreRequest{RepositoryID: "r", Snapshot: "s", Target: "/restore"}
	a := restoreRevision(r, Manifest{Version: 1}, nil)
	r.Confirm = true
	r.Revision = a
	r.ConfirmOverwrite = "/restore"
	if restoreRevision(r, Manifest{Version: 1}, nil) != a {
		t.Fatal("confirmation changed the revision")
	}
	r.Recreate = true
	if restoreRevision(r, Manifest{Version: 1}, nil) == a {
		t.Fatal("recreation not fingerprinted")
	}
}
func TestOutputLimitFailsInsteadOfReturningTruncatedSuccess(t *testing.T) {
	var b limitedBuffer
	_, _ = b.Write(make([]byte, (16<<20)+1))
	if !b.Overflow || b.Len() != 16<<20 {
		t.Fatal("overflow not tracked")
	}
}
func TestRecoveryRestartsOnlyOriginallyRunningAndRejectsMalformedIDs(t *testing.T) {
	s := testService(t)
	id := strings.Repeat("a", 64)
	var mu sync.Mutex
	started := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprintf(w, `{"Id":%q,"State":{"Running":false}}`, id)
		case strings.HasSuffix(r.URL.Path, "/start"):
			mu.Lock()
			started = append(started, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	s.app.Docker = dc
	defer dc.Close()
	cp := stopCheckpoint{Version: 1, Running: []string{id}}
	if e = s.recoverBackup(context.Background(), json.RawMessage(jsonString(cp))); e != nil {
		t.Fatal(e)
	}
	if len(started) != 1 {
		t.Fatal(started)
	}
	cp.Running = []string{"../../../other"}
	if s.recoverBackup(context.Background(), json.RawMessage(jsonString(cp))) == nil {
		t.Fatal("bad checkpoint accepted")
	}
	cp = stopCheckpoint{Version: 99, Running: []string{id}}
	if s.recoverBackup(context.Background(), json.RawMessage(jsonString(cp))) == nil {
		t.Fatal("unknown checkpoint version accepted")
	}
}
func TestOfflineScheduleRecordsMissedWithoutExecuting(t *testing.T) {
	s := testService(t)
	p := Policy{ID: "daily", Name: "test", Enabled: true, Timezone: "UTC", Schedule: "* * * * *"}
	_, e := s.app.DB.Exec(`INSERT INTO backup_policies(id,name,repository_id,definition,created_at) VALUES(?,?,?,?,?)`, p.ID, p.Name, "repo", jsonString(p), now())
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.app.DB.Exec(`INSERT INTO workflow_state VALUES('scheduler_seen',?)`, time.Now().UTC().Add(-4*time.Minute).Format(time.RFC3339Nano))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.recordMissedSchedules(); e != nil {
		t.Fatal(e)
	}
	var count, tasks int
	s.app.DB.QueryRow(`SELECT count(*) FROM schedule_instances WHERE error LIKE 'MISSED_OFFLINE%'`).Scan(&count)
	s.app.DB.QueryRow(`SELECT count(*) FROM tasks`).Scan(&tasks)
	if count < 3 || tasks != 0 {
		t.Fatalf("missed=%d tasks=%d", count, tasks)
	}
	if e = s.recordMissedSchedules(); e != nil {
		t.Fatal(e)
	}
	var next int
	s.app.DB.QueryRow(`SELECT count(*) FROM schedule_instances`).Scan(&next)
	if next != count {
		t.Fatalf("duplicate occurrences: %d != %d", next, count)
	}
}
func TestRecoveredPathsAndManifestMetadataVerification(t *testing.T) {
	s := testService(t)
	m := Manifest{Version: 1, Sources: []backupSource{{Volume: "data", SnapshotPath: "/sources/000"}, {HostPath: "/srv/project", SnapshotPath: "/sources/001"}}}
	req := restoreRequest{Target: "/recovery"}
	q, e := s.recoveredPath(req, m, "volume:data")
	if e != nil || q != "/recovery/sources/000" {
		t.Fatal(q, e)
	}
	q, e = s.recoveredPath(req, m, "/srv/project/nested/.env")
	if e != nil || q != "/recovery/sources/001/nested/.env" {
		t.Fatal(q, e)
	}
	if _, e = s.recoveredPath(req, m, "/srv/project-other"); e == nil {
		t.Fatal("neighbor matched")
	}
	if !overlaps("/a/b", "/a") || overlaps("/a/b", "/a/b-other") {
		t.Fatal("bad writer scope")
	}
	if e = verifyRestored(t.TempDir(), []snapshotNode{{Path: "/missing", Type: "file"}}); e == nil {
		t.Fatal("missing restored file accepted")
	}
}
func waitTask(t *testing.T, m *task.Manager, id string) task.Task {
	t.Helper()
	deadline := time.After(60 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		current, e := m.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if current.Status == "Succeeded" || current.Status == "Failed" || current.Status == "Canceled" {
			return current
		}
		select {
		case <-deadline:
			t.Fatal("task timeout")
		case <-tick.C:
		}
	}
}
func TestResticBackupOfflineDiscoveryAndVerifiedRestore(t *testing.T) {
	if os.Getenv("DM_TEST_RESTIC") == "" {
		t.Skip("set DM_TEST_RESTIC to verified Restic 0.19.1 binary for real CLI integration")
	}
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	repo := filepath.Join(root, "repository")
	source := filepath.Join(root, "source")
	restore := filepath.Join(root, "restore")
	for _, p := range []string{repo, source} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	content := []byte("content survives independent repository recovery\n")
	if e := os.WriteFile(filepath.Join(source, "payload.txt"), content, 0640); e != nil {
		t.Fatal(e)
	}
	secret, e := s.app.Vault.Seal("long-offline-recovery-password")
	if e != nil {
		t.Fatal(e)
	}
	r := Repository{ID: "repo", Name: "test", Location: repo, secret: secret}
	if _, e = s.restic(context.Background(), r, "init"); e != nil {
		t.Fatal(e)
	}
	_, e = s.app.DB.Exec(`INSERT INTO backup_repositories VALUES(?,?,?,?,?,?)`, r.ID, r.Name, r.Location, r.secret, "ready", now())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	p := Policy{ID: "policy", Name: "test", RepositoryID: r.ID, Targets: []Target{{Type: "directory", Path: source}}, Consistency: "file", KeepLast: 2, Timezone: "UTC"}
	job, e := s.app.Tasks.Submit(ctx, "backup", p.ID, "test", "", true, func(t *task.Run) error { return s.backup(t, p) })
	if e != nil {
		t.Fatal(e)
	}
	result := waitTask(t, s.app.Tasks, job.ID)
	if result.Status != "Succeeded" {
		t.Fatal(result.ErrorMessage)
	}
	var ref string
	if e = s.app.DB.QueryRow(`SELECT snapshot_ref FROM backup_snapshots`).Scan(&ref); e != nil {
		t.Fatal(e)
	}
	if _, e = s.app.DB.Exec(`DELETE FROM backup_snapshots`); e != nil {
		t.Fatal(e)
	}
	m, nodes, e := s.readManifest(ctx, r, ref)
	if e != nil {
		t.Fatal(e)
	}
	if m.Version != 1 || len(m.Sources) != 1 {
		t.Fatalf("manifest=%+v", m)
	}
	if _, e = s.restic(ctx, r, "restore", ref, "--target", restore, "--verify"); e != nil {
		t.Fatal(e)
	}
	if e = verifyRestored(restore, dataNodes(m, nodes)); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(filepath.Join(restore, strings.TrimPrefix(m.Sources[0].SnapshotPath, "/"), "payload.txt"))
	if e != nil || string(got) != string(content) {
		t.Fatal(string(got), e)
	}
	if _, e = s.restic(ctx, r, "check", "--read-data"); e != nil {
		t.Fatal(e)
	}
	router := gin.New()
	s.registerBackup(router.Group("/api/v1"))
	apiTarget := filepath.Join(root, "api-recovery")
	restoreBody := map[string]any{"repository_id": r.ID, "snapshot": ref, "target": apiTarget, "overwrite": false}
	previewReq := httptest.NewRequest("POST", "/api/v1/backup/restore/preview", strings.NewReader(jsonString(restoreBody)))
	previewReq.Header.Set("Content-Type", "application/json")
	previewResponse := httptest.NewRecorder()
	router.ServeHTTP(previewResponse, previewReq)
	if previewResponse.Code != 200 {
		t.Fatal(previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Revision string `json:"revision"`
	}
	if e = json.Unmarshal(previewResponse.Body.Bytes(), &preview); e != nil {
		t.Fatal(e)
	}
	restoreBody["revision"] = preview.Revision
	restoreBody["confirm"] = true
	executeReq := httptest.NewRequest("POST", "/api/v1/backup/restore", strings.NewReader(jsonString(restoreBody)))
	executeReq.Header.Set("Content-Type", "application/json")
	executeResponse := httptest.NewRecorder()
	router.ServeHTTP(executeResponse, executeReq)
	if executeResponse.Code != 202 {
		t.Fatal(executeResponse.Code, executeResponse.Body.String())
	}
	var restoreJob task.Task
	if e = json.Unmarshal(executeResponse.Body.Bytes(), &restoreJob); e != nil {
		t.Fatal(e)
	}
	if done := waitTask(t, s.app.Tasks, restoreJob.ID); done.Status != "Succeeded" {
		t.Fatal(done.ErrorMessage)
	}
	fakeEngine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			fmt.Fprint(w, `[]`)
			return
		}
		t.Errorf("unexpected overwrite request %s", r.URL.Path)
		w.WriteHeader(500)
	}))
	defer fakeEngine.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(fakeEngine.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	if e = os.WriteFile(filepath.Join(source, "payload.txt"), []byte("changed after backup"), 0640); e != nil {
		t.Fatal(e)
	}
	overwriteBody := map[string]any{"repository_id": r.ID, "snapshot": ref, "target": source, "overwrite": true}
	overPreview := httptest.NewRequest("POST", "/api/v1/backup/restore/preview", strings.NewReader(jsonString(overwriteBody)))
	overPreview.Header.Set("Content-Type", "application/json")
	overPreviewResponse := httptest.NewRecorder()
	router.ServeHTTP(overPreviewResponse, overPreview)
	if overPreviewResponse.Code != 200 {
		t.Fatal(overPreviewResponse.Code, overPreviewResponse.Body.String())
	}
	if e = json.Unmarshal(overPreviewResponse.Body.Bytes(), &preview); e != nil {
		t.Fatal(e)
	}
	overwriteBody["revision"] = preview.Revision
	overwriteBody["confirm"] = true
	overwriteBody["confirm_overwrite"] = source
	overExecute := httptest.NewRequest("POST", "/api/v1/backup/restore", strings.NewReader(jsonString(overwriteBody)))
	overExecute.Header.Set("Content-Type", "application/json")
	overExecuteResponse := httptest.NewRecorder()
	router.ServeHTTP(overExecuteResponse, overExecute)
	if overExecuteResponse.Code != 202 {
		t.Fatal(overExecuteResponse.Code, overExecuteResponse.Body.String())
	}
	if e = json.Unmarshal(overExecuteResponse.Body.Bytes(), &restoreJob); e != nil {
		t.Fatal(e)
	}
	if done := waitTask(t, s.app.Tasks, restoreJob.ID); done.Status != "Succeeded" {
		t.Fatal(done.ErrorMessage)
	}
	got, e = os.ReadFile(filepath.Join(source, "payload.txt"))
	if e != nil || string(got) != string(content) {
		t.Fatal("original file not restored in place", string(got), e)
	}
	var manifestInDB string
	s.app.DB.QueryRow(`SELECT manifest FROM backup_snapshots LIMIT 1`).Scan(&manifestInDB)
	if strings.Contains(manifestInDB, "long-offline") {
		t.Fatal("secret in snapshot index")
	}
}

func TestMain(m *testing.M) {
	if handleLazyWorkerRestic() {
		os.Exit(0)
	}
	if handleProcessHelper() {
		os.Exit(0)
	}
	gin.SetMode(gin.TestMode)
	if os.Getenv("WORKFLOW_TEST_HELPER") == "1" {
		_ = os.WriteFile(os.Getenv("WORKFLOW_TEST_RECORD"), []byte(jsonString(os.Args[1:])), 0600)
		if config := os.Getenv("WORKFLOW_TEST_COMPOSE_CONFIG"); config != "" {
			fmt.Fprintln(os.Stdout, config)
			os.Exit(0)
		}
		if len(os.Args) > 1 && os.Args[1] == "backup" {
			fmt.Fprintf(os.Stdout, `{"message_type":"summary","snapshot_id":%q}`+"\n", strings.Repeat("a", 64))
		} else if len(os.Args) > 1 && os.Args[1] == "ls" {
			fmt.Fprintln(os.Stdout, `{"struct_type":"node","name":"data","path":"/data","type":"dir"}`)
		} else {
			fmt.Fprintln(os.Stdout, `{"services":{"web":{"image":"alpine:3.22"}}}`)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestComposeImportDoesNotDeployAndEncryptsEnvironment(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	project := filepath.Join(root, "stack with spaces")
	if e := os.Mkdir(project, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(project, "compose.yaml")
	if e := os.WriteFile(file, []byte("services: {}\n"), 0644); e != nil {
		t.Fatal(e)
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	s.app.Config.ComposePath = executable
	record := filepath.Join(root, "arguments.json")
	p := Project{Name: "stack", Workdir: project, ConfigPaths: []string{file}, Environment: map[string]string{"WORKFLOW_TEST_HELPER": "1", "WORKFLOW_TEST_RECORD": record, "DATABASE_PASSWORD": "do-not-store-plaintext"}}
	router := gin.New()
	s.registerCompose(router.Group("/api/v1"))
	body := map[string]any{"name": p.Name, "workdir": p.Workdir, "config_paths": p.ConfigPaths, "environment": p.Environment, "confirm": true}
	request := httptest.NewRequest("POST", "/api/v1/compose/import", strings.NewReader(jsonString(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	args, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(args), `"up"`) || !strings.Contains(string(args), `"config"`) {
		t.Fatal(string(args))
	}
	var stored string
	if e = s.app.DB.QueryRow(`SELECT environment FROM compose_projects`).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(stored, "do-not-store-plaintext") {
		t.Fatal("plaintext environment in database")
	}
	plain, e := s.app.Vault.Open(stored)
	if e != nil || !strings.Contains(plain, "do-not-store-plaintext") {
		t.Fatal("environment was not preserved")
	}
}
func TestSecretExportIsExplicitAndNeverAuditedAsContent(t *testing.T) {
	s := testService(t)
	password := "offline-secret-no-logs"
	sealed, e := s.app.Vault.Seal(password)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.app.DB.Exec(`INSERT INTO backup_repositories VALUES('repo','test','/authorized/repo',?,'ready',?)`, sealed, now())
	if e != nil {
		t.Fatal(e)
	}
	router := gin.New()
	s.registerBackup(router.Group("/api/v1"))
	for _, test := range []struct {
		body   string
		status int
		secret bool
	}{{`{"confirm":false}`, 400, false}, {`{"confirm":true}`, 200, true}} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/v1/backup/repositories/repo/export", strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != test.status || strings.Contains(response.Body.String(), password) != test.secret {
			t.Fatal(response.Code, response.Body.String())
		}
		if test.secret && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("secret response was cacheable")
		}
	}
	rows, e := s.app.DB.Query(`SELECT action,resource_id,result FROM audit_logs`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var action, target, result string
		if e = rows.Scan(&action, &target, &result); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(action+target+result, password) {
			t.Fatal("secret in audit")
		}
	}
}
func TestManifestRejectsUntrustedRootAndTraversal(t *testing.T) {
	for _, path := range []string{"/", "relative", "/sources/../escape", "/sources/../../etc"} {
		m := Manifest{Version: 1, Sources: []backupSource{{Volume: "data", SnapshotPath: path}}}
		if validateManifest(m) == nil {
			t.Fatal("unsafe source accepted:", path)
		}
	}
}

func TestVolumeWorkerMountsAreExplicitReadOnlyAndUnprivileged(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	repo := filepath.Join(root, "repo")
	stage := filepath.Join(root, "stage")
	for _, p := range []string{repo, stage} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	workerID := strings.Repeat("b", 64)
	var got struct {
		HostConfig container.HostConfig
		Env        []string
		Entrypoint []string
		Cmd        []string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			if e := json.NewDecoder(r.Body).Decode(&got); e != nil {
				t.Error(e)
			}
			fmt.Fprintf(w, `{"Id":%q}`, workerID)
		case strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/wait"):
			fmt.Fprint(w, `{"StatusCode":0}`)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			writer := stdcopy.NewStdWriter(w, stdcopy.Stdout)
			fmt.Fprintln(writer, `{"message_type":"summary","snapshot_id":"committed"}`)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected worker API %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "test.worker", "volume", "test", "", false, func(run *task.Run) error {
		cp := stopCheckpoint{Version: 1}
		b, e := s.backupFiles(run, Repository{Location: repo}, []backupSource{{Volume: "data", HostPath: "/var/lib/docker/volumes/data/_data", SnapshotPath: "/sources/000"}}, stage, "sensitive-password", &cp, Policy{ID: "p"})
		if e != nil {
			return e
		}
		_, e = decodeLines(b, "summary")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	result := waitTask(t, s.app.Tasks, job.ID)
	if result.Status != "Succeeded" {
		t.Fatal(result.ErrorMessage)
	}
	if os.Geteuid() != 0 && !reflect.DeepEqual(got.Entrypoint, []string{"/usr/local/bin/docklane", "backup-worker"}) {
		t.Fatalf("backup worker must invoke the executable shipped in the image: %v", got.Entrypoint)
	}
	if got.HostConfig.Privileged || !got.HostConfig.ReadonlyRootfs || got.HostConfig.NetworkMode != "none" || (len(got.HostConfig.CapDrop) != 1 || got.HostConfig.CapDrop[0] != "ALL") {
		t.Fatalf("unsafe worker: %+v", got.HostConfig)
	}
	caps := map[string]bool{}
	for _, cap := range got.HostConfig.CapAdd {
		caps[strings.TrimPrefix(cap, "CAP_")] = true
	}
	if !caps["DAC_READ_SEARCH"] {
		t.Fatal("worker cannot read application-owned source files")
	}
	for cap := range caps {
		if cap != "DAC_READ_SEARCH" && cap != "SETUID" && cap != "SETGID" && cap != "SETPCAP" {
			t.Fatal("unexpected worker capability:", cap)
		}
	}
	if len(got.HostConfig.Mounts) != 3 {
		t.Fatal(got.HostConfig.Mounts)
	}
	for _, required := range []string{"HOME=/tmp", "TMPDIR=/tmp", "RESTIC_CACHE_DIR=/tmp/restic-cache"} {
		if !strings.Contains("\n"+strings.Join(got.Env, "\n")+"\n", "\n"+required+"\n") {
			t.Fatalf("readonly worker cache configuration missing: %s", required)
		}
	}
	for _, mp := range got.HostConfig.Mounts {
		if mp.Source == "/" || strings.Contains(mp.Source, "docker.sock") {
			t.Fatal("forbidden worker mount")
		}
		if mp.Target == "/sources/000" && (!mp.ReadOnly || mp.Source != "data") {
			t.Fatal("source must be explicitly selected read-only volume")
		}
	}
}

func TestRestoreWorkerInvokesDocklaneExecutable(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	repo, target := filepath.Join(root, "repo"), filepath.Join(root, "target")
	for _, dir := range []string{repo, target} {
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	secret, e := s.app.Vault.Seal("worker-test-secret")
	if e != nil {
		t.Fatal(e)
	}
	var got container.Config
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/create") {
			t.Errorf("unexpected worker API %s", r.URL.Path)
		}
		if e := json.NewDecoder(r.Body).Decode(&got); e != nil {
			t.Error(e)
		}
		http.Error(w, "fixture rejects create", http.StatusInternalServerError)
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "test.restore-worker", "snapshot", "test", "", false, func(run *task.Run) error {
		cp := stopCheckpoint{Version: 1}
		return s.restoreInWorker(run, Repository{Location: repo, secret: secret}, restoreRequest{Snapshot: strings.Repeat("a", 64)}, Manifest{}, []snapshotNode{{Path: "/file", Type: "file"}}, target, &cp)
	})
	if e != nil {
		t.Fatal(e)
	}
	result := waitTask(t, s.app.Tasks, job.ID)
	if result.Status != "Failed" || !strings.Contains(result.ErrorMessage, "fixture rejects create") {
		t.Fatalf("worker did not reach container creation: %+v", result)
	}
	if !reflect.DeepEqual([]string(got.Entrypoint), []string{"/usr/local/bin/docklane", "restore-worker", "/spec/spec.json"}) {
		t.Fatalf("restore worker must invoke the executable shipped in the image: %v", got.Entrypoint)
	}
}

func TestWorkerArgumentBoundaryRejectsArbitraryResticFlags(t *testing.T) {
	good := []string{"backup", "--json", "--tag", "dm-policy:123", "--exclude", "*.tmp", "/manifest/manifest.json", "/sources/000"}
	if e := validateWorkerArgs(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]string{{"restore", "--target", "/"}, {"backup", "--json", "--repository", "/etc", "/manifest/manifest.json", "/sources/000"}, {"backup", "--json", "/manifest/manifest.json", "/sources/../etc"}, {"backup", "--json", "/manifest/manifest.json", "/etc"}} {
		if validateWorkerArgs(bad) == nil {
			t.Fatal("unsafe worker argv accepted:", bad)
		}
	}
}

func TestContainerRecreationUsesRecoveredFilesAndPreservesConfiguration(t *testing.T) {
	s := testService(t)
	oldID := strings.Repeat("c", 64)
	imageID := "sha256:" + strings.Repeat("d", 64)
	newID := strings.Repeat("e", 64)
	target := filepath.Join(s.app.Config.AllowedPaths[0], "recovered")
	var created struct {
		Env        []string
		Cmd        []string
		Image      string
		Labels     map[string]string
		HostConfig container.HostConfig
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			fmt.Fprint(w, `[]`)
		case strings.Contains(r.URL.Path, "/images/"):
			fmt.Fprintf(w, `{"Id":%q}`, imageID)
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			if e := json.NewDecoder(r.Body).Decode(&created); e != nil {
				t.Error(e)
			}
			fmt.Fprintf(w, `{"Id":%q}`, newID)
		case strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/containers/"+newID+"/json"):
			fmt.Fprintf(w, `{"Id":%q,"State":{"Running":true,"Health":{"Status":"healthy"}}}`, newID)
		default:
			t.Errorf("unexpected recreate request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	raw := json.RawMessage(fmt.Sprintf(`{"Id":%q,"Name":"/database","Image":%q,"Config":{"Image":"database:old","Env":["DATABASE_PASSWORD=encrypted-in-manifest"],"Cmd":["server","--flag"],"Labels":{}},"HostConfig":{"Binds":["old-data:/data"],"RestartPolicy":{"Name":"unless-stopped"}},"Mounts":[{"Type":"volume","Name":"old-data","Destination":"/data","RW":true}]}`, oldID, imageID))
	m := Manifest{Version: 1, Sources: []backupSource{{Volume: "old-data", HostPath: "/var/lib/docker/volumes/old-data/_data", SnapshotPath: "/sources/000"}}, Containers: []json.RawMessage{raw}}
	req := restoreRequest{Target: target, Snapshot: strings.Repeat("f", 64), Recreate: true}
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "test.recreate", "database", "test", "", false, func(run *task.Run) error {
		cp := stopCheckpoint{Version: 1}
		return s.recreateResources(run, req, m, &cp)
	})
	if e != nil {
		t.Fatal(e)
	}
	done := waitTask(t, s.app.Tasks, job.ID)
	if done.Status != "Succeeded" {
		t.Fatal(done.ErrorMessage)
	}
	if created.Image != imageID || len(created.Env) != 1 || created.Env[0] != "DATABASE_PASSWORD=encrypted-in-manifest" || !reflect.DeepEqual(created.Cmd, []string{"server", "--flag"}) {
		t.Fatal("configuration changed")
	}
	if len(created.HostConfig.Binds) != 0 || len(created.HostConfig.Mounts) != 1 || created.HostConfig.Mounts[0].Source != filepath.Join(target, "sources/000") || created.HostConfig.Mounts[0].Target != "/data" {
		t.Fatal("data did not switch to recovered path")
	}
	if created.Labels["docker-manager.recovery"] != job.ID {
		t.Fatal("missing recovery ownership label")
	}
	var checkpoint string
	if e = s.app.DB.QueryRow(`SELECT checkpoint FROM tasks WHERE id=?`, job.ID).Scan(&checkpoint); e != nil {
		t.Fatal(e)
	}
	var cp stopCheckpoint
	json.Unmarshal([]byte(checkpoint), &cp)
	if len(cp.Created) != 0 || cp.RecoveryTask != "" {
		t.Fatal("successful recreation left destructive recovery checkpoint")
	}
}

func TestComposeRestorePreflightIsRepeatableWithoutMutatingManifest(t *testing.T) {
	s := testService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			fmt.Fprint(w, `[]`)
			return
		}
		if strings.Contains(r.URL.Path, "/images/") {
			fmt.Fprint(w, `{"Id":"image"}`)
			return
		}
		t.Errorf("unexpected path %s", r.URL.Path)
		w.WriteHeader(500)
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	m := Manifest{Version: 1, ComposeConfig: map[string]json.RawMessage{"p": json.RawMessage(`{"services":{"web":{"image":"example:1"}}}`)}, Sources: []backupSource{{HostPath: "/srv/project", SnapshotPath: "/sources/000"}}, Compose: []Project{{ID: "p", Name: "stack", Workdir: "/srv/project", ConfigPaths: []string{"/srv/project/base.yaml", "/srv/project/override.yaml"}, EnvFiles: []string{"/srv/project/production.env"}, Profiles: []string{"jobs"}}}}
	m.Containers = []json.RawMessage{json.RawMessage(`{"Id":"original","Name":"/stack-web-1","Image":"image","Config":{"Labels":{"com.docker.compose.project":"stack","com.docker.compose.service":"web"}},"HostConfig":{},"Mounts":[]}`)}
	before := jsonString(m)
	req := restoreRequest{Recreate: true, Snapshot: strings.Repeat("a", 64), Target: "/recovery"}
	for i := 0; i < 2; i++ {
		plan, e := s.resourcePreflight(context.Background(), req, m)
		if e != nil {
			t.Fatal(e)
		}
		if len(plan.Projects) != 1 || plan.Projects[0].ConfigPaths[0] != "/recovery/sources/000/base.yaml" {
			t.Fatal(plan)
		}
	}
	if jsonString(m) != before {
		t.Fatal("restore preflight mutated the encrypted manifest context")
	}
}

func TestCommittedSnapshotWithoutManifestFailsBeforeRetention(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	repo := filepath.Join(root, "repo")
	source := filepath.Join(root, "source")
	for _, dir := range []string{repo, source} {
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	sealed, e := s.app.Vault.Seal("secret-must-not-log")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.app.DB.Exec(`INSERT INTO backup_repositories VALUES('repo','repo',?,?,'ready',?)`, repo, sealed, now())
	if e != nil {
		t.Fatal(e)
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	s.app.Config.ResticPath = executable
	record := filepath.Join(root, "cli-args")
	t.Setenv("WORKFLOW_TEST_HELPER", "1")
	t.Setenv("WORKFLOW_TEST_RECORD", record)
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	p := Policy{ID: "p", Name: "test", RepositoryID: "repo", Targets: []Target{{Type: "directory", Path: source}}, Consistency: "file", KeepLast: 1}
	job, e := s.app.Tasks.Submit(ctx, "backup", p.ID, "test", "", false, func(run *task.Run) error { return s.backup(run, p) })
	if e != nil {
		t.Fatal(e)
	}
	done := waitTask(t, s.app.Tasks, job.ID)
	if done.Status != "Failed" || !strings.Contains(done.ErrorMessage, "manifest") {
		t.Fatal(done.Status, done.ErrorMessage)
	}
	var snapshots int
	s.app.DB.QueryRow(`SELECT count(*) FROM backup_snapshots`).Scan(&snapshots)
	if snapshots != 0 {
		t.Fatal("unrestorable snapshot was marked verified")
	}
	last, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(last), `"ls"`) || strings.Contains(string(last), `"forget"`) {
		t.Fatal("retention ran after missing manifest", string(last))
	}
}
func TestStagingCleanupFailureDoesNotSkipOriginalWriterRestart(t *testing.T) {
	s := testService(t)
	id := strings.Repeat("a", 64)
	starts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			fmt.Fprint(w, `{"State":{"Running":false}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/start") {
			starts++
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(500)
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	cp := stopCheckpoint{Version: 1, Running: []string{id}, Stage: filepath.Join(s.app.Config.AllowedPaths[0], "not-a-staging-directory")}
	if e = s.recoverBackup(context.Background(), json.RawMessage(jsonString(cp))); e == nil {
		t.Fatal("invalid staging directory should require attention")
	}
	if starts != 1 {
		t.Fatal("original writer was not restarted after unrelated staging cleanup error")
	}
}

func TestComposeBackupIncludesExternalConfigsAndEnvironmentFiles(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	workdir := filepath.Join(root, "project")
	shared := filepath.Join(root, "shared")
	for _, dir := range []string{workdir, shared} {
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	base := filepath.Join(workdir, "base.yaml")
	override := filepath.Join(shared, "override.yaml")
	env := filepath.Join(shared, "production.env")
	serviceEnv := filepath.Join(shared, "service.env")
	configFile := filepath.Join(shared, "db.conf")
	for _, path := range []string{base, override, env, serviceEnv, configFile} {
		if e := os.WriteFile(path, []byte("test"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	compiled := map[string]any{"services": map[string]any{"web": map[string]any{"env_file": []any{map[string]any{"path": serviceEnv, "required": true}}}}, "configs": map[string]any{"db": map[string]any{"file": configFile}}}
	p := Project{ID: "project", Name: "stack", Workdir: workdir, ConfigPaths: []string{base, override}, EnvFiles: []string{env}, Environment: map[string]string{"WORKFLOW_TEST_HELPER": "1", "WORKFLOW_TEST_RECORD": filepath.Join(root, "arguments"), "WORKFLOW_TEST_COMPOSE_CONFIG": jsonString(compiled)}}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	s.app.Config.ComposePath = executable
	sealed, e := s.app.Vault.Seal(jsonString(p.Environment))
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.app.DB.Exec(`INSERT INTO compose_projects(id,name,source_type,workdir,config_paths,environment,env_files,profiles,revision,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, "local", p.Workdir, jsonString(p.ConfigPaths), sealed, jsonString(p.EnvFiles), "[]", "preview", now())
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) }))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	sources, _, _, e := s.sources(context.Background(), Policy{Targets: []Target{{Type: "compose", Name: p.ID}}})
	if e != nil {
		t.Fatal(e)
	}
	included := map[string]bool{}
	for _, source := range sources {
		included[filepath.Base(source.HostPath)] = true
	}
	for _, name := range []string{"project", "override.yaml", "production.env", "service.env", "db.conf"} {
		if !included[name] {
			t.Fatal("dependency absent from backup sources:", name, sources)
		}
	}
}

func TestOverwriteRecoveryKeepsWritersStoppedUntilValidation(t *testing.T) {
	s := testService(t)
	id := strings.Repeat("a", 64)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if strings.HasSuffix(r.URL.Path, "/json") {
			fmt.Fprint(w, `{"State":{"Running":false}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/start") {
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(500)
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	cp := stopCheckpoint{Version: 1, Running: []string{id}, HoldWriters: true}
	if e = s.recoverBackup(context.Background(), json.RawMessage(jsonString(cp))); e != nil {
		t.Fatal(e)
	}
	if requests != 0 {
		t.Fatal("partial overwrite recovery restarted an application")
	}
	cp.HoldWriters = false
	if e = s.recoverBackup(context.Background(), json.RawMessage(jsonString(cp))); e != nil {
		t.Fatal(e)
	}
	if requests != 2 {
		t.Fatal("validated restore did not resume original application")
	}
}
func TestManualBackupDoesNotStopNewUnapprovedSharedWriter(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	repo := filepath.Join(root, "repo")
	source := filepath.Join(root, "source")
	for _, dir := range []string{repo, source} {
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	sealed, e := s.app.Vault.Seal("repo-secret")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.app.DB.Exec(`INSERT INTO backup_repositories VALUES('repo','repo',?,?,'ready',?)`, repo, sealed, now())
	if e != nil {
		t.Fatal(e)
	}
	newWriter := strings.Repeat("b", 64)
	unapprovedActions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			fmt.Fprintf(w, `[{"Id":%q,"State":"running","Mounts":[{"Type":"bind","Source":%q,"Destination":"/data","RW":true}]}]`, newWriter, source)
			return
		}
		unapprovedActions++
		w.WriteHeader(500)
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	p := Policy{ID: "p", Name: "test", RepositoryID: "repo", Targets: []Target{{Type: "directory", Path: source}}, Consistency: "stop", KeepLast: 1}
	sources, _, _, e := s.sources(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "backup", p.ID, "test", "", false, func(run *task.Run) error {
		return s.backup(run, p, backupApproval{Sources: sources, Writers: []string{strings.Repeat("a", 64)}})
	})
	if e != nil {
		t.Fatal(e)
	}
	done := waitTask(t, s.app.Tasks, job.ID)
	if done.Status != "Failed" || !strings.Contains(done.ErrorMessage, "scope changed") {
		t.Fatal(done.Status, done.ErrorMessage)
	}
	if unapprovedActions != 0 {
		t.Fatal("unapproved writer was stopped or restarted")
	}
}

func TestWorkerCleanupFailureRetainsIdentityAndHoldsOriginalWriter(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	repo := filepath.Join(root, "repo")
	stage := filepath.Join(root, "stage")
	for _, dir := range []string{repo, stage} {
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	worker := strings.Repeat("b", 64)
	original := strings.Repeat("a", 64)
	allowRemove := false
	starts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			fmt.Fprintf(w, `{"Id":%q}`, worker)
		case strings.HasSuffix(r.URL.Path, "/start"):
			if strings.Contains(r.URL.Path, original) {
				starts++
			}
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/wait"):
			fmt.Fprint(w, `{"StatusCode":0}`)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			writer := stdcopy.NewStdWriter(w, stdcopy.Stdout)
			fmt.Fprintln(writer, `{"message_type":"summary","snapshot_id":"committed"}`)
		case r.Method == "DELETE":
			if allowRemove {
				w.WriteHeader(204)
			} else {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"message":"engine unavailable during cleanup"}`)
			}
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprint(w, `{"State":{"Running":false}}`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "test.worker", "volume", "test", "", false, func(run *task.Run) error {
		cp := stopCheckpoint{Version: 1, Running: []string{original}}
		_, e := s.backupFiles(run, Repository{Location: repo}, []backupSource{{Volume: "data", SnapshotPath: "/sources/000"}}, stage, "secret", &cp, Policy{ID: "p"})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	done := waitTask(t, s.app.Tasks, job.ID)
	if done.Status != "Failed" {
		t.Fatal("worker removal failure incorrectly succeeded")
	}
	var checkpoint string
	if e = s.app.DB.QueryRow(`SELECT checkpoint FROM tasks WHERE id=?`, job.ID).Scan(&checkpoint); e != nil {
		t.Fatal(e)
	}
	var cp stopCheckpoint
	if e = json.Unmarshal([]byte(checkpoint), &cp); e != nil {
		t.Fatal(e)
	}
	if cp.Worker != worker {
		t.Fatal("worker identity lost after failed removal")
	}
	if e = s.recoverBackup(ctx, json.RawMessage(checkpoint)); e == nil {
		t.Fatal("running worker removal should still fail")
	}
	if starts != 0 {
		t.Fatal("writers resumed while worker may still be active")
	}
	allowRemove = true
	if e = s.recoverBackup(ctx, json.RawMessage(checkpoint)); e != nil {
		t.Fatal(e)
	}
	if starts != 1 {
		t.Fatal("writer did not resume after successful worker removal")
	}
}

func TestComposeRecreationNeverStartsUncapturedServices(t *testing.T) {
	s := testService(t)
	root := s.app.Config.AllowedPaths[0]
	target := filepath.Join(root, "recovery")
	work := filepath.Join(target, "sources/000")
	if e := os.MkdirAll(work, 0700); e != nil {
		t.Fatal(e)
	}
	record := filepath.Join(root, "compose-args")
	t.Setenv("WORKFLOW_TEST_HELPER", "1")
	t.Setenv("WORKFLOW_TEST_RECORD", record)
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	s.app.Config.ComposePath = executable
	imageID := "sha256:" + strings.Repeat("a", 64)
	ctID := strings.Repeat("b", 64)
	configPath := filepath.Join(work, ".docker-manager-recovery.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/"):
			fmt.Fprintf(w, `{"Id":%q}`, imageID)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			args, _ := os.ReadFile(record)
			if !strings.Contains(string(args), `"up"`) {
				fmt.Fprint(w, `[]`)
				return
			}
			b, e := os.ReadFile(configPath)
			if e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			var generated map[string]any
			json.Unmarshal(b, &generated)
			services := generated["services"].(map[string]any)
			known := services["known"].(map[string]any)
			labels := known["labels"].(map[string]any)
			fmt.Fprintf(w, `[{"Id":%q,"Labels":{"docker-manager.recovery":%q,"com.docker.compose.project":%q,"com.docker.compose.service":"known"}}]`, ctID, labels["docker-manager.recovery"], generated["name"])
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprint(w, `{"State":{"Running":true,"Health":{"Status":"healthy"}}}`)
		default:
			t.Errorf("unexpected compose restore API %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	raw := json.RawMessage(fmt.Sprintf(`{"Id":"old","Name":"/stack-known-1","Image":%q,"Config":{"Labels":{"com.docker.compose.project":"stack","com.docker.compose.service":"known"}},"HostConfig":{},"Mounts":[{"Type":"bind","Source":"/live/safe","Destination":"/data","RW":true}]}`, imageID))
	compiled := json.RawMessage(`{"services":{"known":{"image":"known:1","volumes":[{"type":"bind","source":"/live/safe","target":"/data"}]},"undeployed":{"image":"unsafe:1","volumes":[{"type":"bind","source":"/live/unsafe","target":"/host"}]}},"networks":{"default":{"name":"stack_default"}}}`)
	m := Manifest{Version: 1, Sources: []backupSource{{HostPath: "/original/project", SnapshotPath: "/sources/000"}, {HostPath: "/live/safe", SnapshotPath: "/sources/001"}}, Containers: []json.RawMessage{raw}, Compose: []Project{{ID: "p", Name: "stack", Workdir: "/original/project", ConfigPaths: []string{"base.yaml"}}}, ComposeConfig: map[string]json.RawMessage{"p": compiled}}
	req := restoreRequest{Target: target, Snapshot: strings.Repeat("a", 64), Recreate: true}
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "test.compose.restore", "stack", "test", "", false, func(run *task.Run) error {
		cp := stopCheckpoint{Version: 1}
		return s.recreateResources(run, req, m, &cp)
	})
	if e != nil {
		t.Fatal(e)
	}
	done := waitTask(t, s.app.Tasks, job.ID)
	if done.Status != "Succeeded" {
		t.Fatal(done.ErrorMessage)
	}
	generated, e := os.ReadFile(configPath)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(generated), "undeployed") || strings.Contains(string(generated), "/live/") {
		t.Fatal("recovery retained unapproved live data/service references", string(generated))
	}
	args, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(args), `"--no-deps"`) || !strings.Contains(string(args), `"known"`) || strings.Contains(string(args), `"undeployed"`) {
		t.Fatal("up was not restricted to captured services", string(args))
	}
}
func TestWorkerFailureLogsAreBoundedAndSecretsRedacted(t *testing.T) {
	s := testService(t)
	password := "rawrepositorycredential"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(stdcopy.NewStdWriter(w, stdcopy.Stdout), "PRIVATE_MANIFEST_CONTENT")
		fmt.Fprintln(stdcopy.NewStdWriter(w, stdcopy.Stderr), "fatal "+password+" token=secondcredential")
	}))
	defer server.Close()
	dc, e := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.51"))
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	s.app.Docker = dc
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "test.logs", "worker", "test", "", false, func(run *task.Run) error {
		return s.workerFailure(run, strings.Repeat("a", 64), "worker failed", password)
	})
	if e != nil {
		t.Fatal(e)
	}
	if done := waitTask(t, s.app.Tasks, job.ID); done.Status != "Failed" {
		t.Fatal(done.Status)
	}
	logs, e := s.app.Tasks.Logs(job.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, log := range logs {
		for _, secret := range []string{password, "secondcredential", "PRIVATE_MANIFEST_CONTENT"} {
			if strings.Contains(log.Message, secret) {
				t.Fatal("sensitive worker data in logs")
			}
		}
	}
}

func TestDaemonFailureCompensationIsNotRepeatedByTaskManager(t *testing.T) {
	s := testService(t)
	path := filepath.Join(s.app.Config.AllowedPaths[0], "daemon.json")
	original := []byte(`{"log-driver":"local"}`)
	if e := os.WriteFile(path, []byte(`{"registry-mirrors":["https://broken.example"]}`), 0600); e != nil {
		t.Fatal(e)
	}
	restores, restarts := 0, 0
	recovery := func(_ context.Context, raw json.RawMessage) error {
		var cp daemonCheckpoint
		if e := json.Unmarshal(raw, &cp); e != nil {
			return e
		}
		if !cp.Written {
			return nil
		}
		restores++
		if e := os.WriteFile(path, original, 0600); e != nil {
			return e
		}
		restarts++
		return nil
	}
	s.app.Tasks.Recover("daemon.configure", recovery)
	ctx, cancel := context.WithCancel(context.Background())
	if e := s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	job, e := s.app.Tasks.Submit(ctx, "daemon.configure", "local", "test", "", false, func(run *task.Run) (err error) {
		cp := daemonCheckpoint{Version: 1, Path: path, Written: true}
		if e := run.Checkpoint(cp); e != nil {
			return e
		}
		defer func() {
			if err != nil {
				err = errors.Join(err, completeDaemonCompensation(run, &cp, recovery))
			}
		}()
		return errors.New("requested mirror did not become active")
	})
	if e != nil {
		t.Fatal(e)
	}
	done := waitTask(t, s.app.Tasks, job.ID)
	if done.Status != "Failed" {
		t.Fatal(done.Status)
	}
	if restores != 1 || restarts != 1 {
		t.Fatalf("rollback/restart repeated: restores=%d restarts=%d", restores, restarts)
	}
	var raw string
	if e = s.app.DB.QueryRow(`SELECT checkpoint FROM tasks WHERE id=?`, job.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var cp daemonCheckpoint
	if e = json.Unmarshal([]byte(raw), &cp); e != nil {
		t.Fatal(e)
	}
	if cp.Written {
		t.Fatal("completed rollback not persisted")
	}
	if e = recovery(ctx, json.RawMessage(raw)); e != nil {
		t.Fatal(e)
	}
	if restores != 1 || restarts != 1 {
		t.Fatal("persisted checkpoint repeated daemon restart")
	}
	got, e := os.ReadFile(path)
	if e != nil || string(got) != string(original) {
		t.Fatal("original configuration not restored", e)
	}
}
func TestDaemonRollbackCheckpointFailureRequiresCompensationGate(t *testing.T) {
	s := testService(t)
	_, e := s.app.DB.Exec(`CREATE TRIGGER reject_daemon_completion BEFORE UPDATE OF checkpoint ON tasks WHEN json_extract(NEW.checkpoint,'$.written')=0 BEGIN SELECT RAISE(FAIL,'checkpoint unavailable'); END`)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if e = s.app.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); s.app.Tasks.Wait() }()
	restores := 0
	observed := make(chan error, 1)
	job, e := s.app.Tasks.Submit(ctx, "test.daemon.checkpoint", "local", "test", "", false, func(run *task.Run) error {
		cp := daemonCheckpoint{Version: 1, Written: true}
		if e := run.Checkpoint(cp); e != nil {
			return e
		}
		err := completeDaemonCompensation(run, &cp, func(_ context.Context, _ json.RawMessage) error { restores++; return nil })
		observed <- err
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
	done := waitTask(t, s.app.Tasks, job.ID)
	if done.Status != "Failed" {
		t.Fatal(done.Status)
	}
	err := <-observed
	var gate interface{ RecoveryRequired() bool }
	if !errors.As(err, &gate) || !gate.RecoveryRequired() {
		t.Fatal("checkpoint failure did not require a compensation gate", err)
	}
	if restores != 1 {
		t.Fatal("rollback body repeated inside failed checkpoint completion")
	}
	var raw string
	if e = s.app.DB.QueryRow(`SELECT checkpoint FROM tasks WHERE id=?`, job.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var cp daemonCheckpoint
	if e = json.Unmarshal([]byte(raw), &cp); e != nil {
		t.Fatal(e)
	}
	if !cp.Written {
		t.Fatal("failed checkpoint save falsely cleared pending rollback")
	}
}
