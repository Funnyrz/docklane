package resources

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func testApp(t *testing.T, engine http.HandlerFunc, middleware ...gin.HandlerFunc) (*platform.App, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	docker, e := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithVersion("1.51"), client.WithHTTPClient(srv.Client()))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = docker.Close() })
	dir := t.TempDir()
	db, e := database.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	vault, e := security.NewVault(dir)
	if e != nil {
		t.Fatal(e)
	}
	a := &platform.App{DB: db, Docker: docker, Vault: vault, Tasks: task.New(db), Config: config.Config{Mode: "binary", DataDir: dir, AllowedPaths: []string{dir}}}
	r := gin.New()
	r.Use(middleware...)
	Register(r.Group("/api/v1"), a)
	ctx, cancel := context.WithCancel(context.Background())
	if e = a.Tasks.Start(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); a.Tasks.Wait(); _ = db.Close() })
	return a, r
}
func request(t *testing.T, r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var e error
		data, e = json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func enginePath(r *http.Request) string { return strings.TrimPrefix(r.URL.Path, "/v1.51") }
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func original() container.InspectResponse {
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: strings.Repeat("a", 64), Name: "/app", Image: "sha256:old", Platform: "linux", HostConfig: &container.HostConfig{}, State: &container.State{Running: true, Status: "running"}}, Config: &container.Config{Image: "busybox:old", Env: []string{"SECRET=privatevalue"}}, Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "anonymous-data", Destination: "/data", RW: true}}}
}
func awaitTask(t *testing.T, a *platform.App, id string) task.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, e := a.Tasks.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if v.Status == "Succeeded" || v.Status == "Failed" || v.Status == "Canceled" {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return task.Task{}
}
func taskID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != 202 {
		t.Fatalf("expected 202, got %d %s", w.Code, w.Body.String())
	}
	var v task.Task
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	return v.ID
}

func TestExternalContainersPaginationAndHostBoundary(t *testing.T) {
	_, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Query().Get("all") != "1" {
			t.Error("must include stopped containers")
		}
		writeJSON(w, []container.Summary{{ID: "external-cli", Names: []string{"/cli"}, Image: "nginx", State: "exited"}, {ID: "third-party", Names: []string{"/third"}, Image: "redis", State: "running"}})
	})
	w := request(t, r, "GET", "/api/v1/hosts/local/containers?page_size=1&state=exited", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "external-cli") || strings.Contains(w.Body.String(), "third-party") {
		t.Fatalf("unexpected list: %d %s", w.Code, w.Body.String())
	}
	w = request(t, r, "GET", "/api/v1/hosts/remote/containers", nil)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestDeleteContainerKeepsVolumes(t *testing.T) {
	var deleted bool
	var mu sync.Mutex
	a, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.Method == "DELETE" {
			if q.URL.Query().Get("v") == "1" || q.URL.Query().Get("force") == "1" {
				t.Error("unsafe container delete options")
			}
			deleted = true
			w.WriteHeader(204)
			return
		}
		if deleted {
			w.WriteHeader(404)
			writeJSON(w, gin.H{"message": "not found"})
			return
		}
		v := original()
		v.State.Running = false
		writeJSON(w, v)
	})
	w := request(t, r, "DELETE", "/api/v1/hosts/local/containers/app", gin.H{"confirmed": true})
	result := awaitTask(t, a, taskID(t, w))
	if result.Status != "Succeeded" {
		t.Fatal(result.ErrorMessage)
	}
}
func TestStoppedVolumeReferencePreventsDelete(t *testing.T) {
	deleted := false
	_, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "DELETE" {
			deleted = true
		}
		writeJSON(w, []container.Summary{{ID: "stopped", State: "exited", Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "data"}}}})
	})
	w := request(t, r, "DELETE", "/api/v1/hosts/local/volumes/data", gin.H{"confirmed": true})
	if w.Code != 409 || deleted {
		t.Fatalf("referenced volume delete: %d %s", w.Code, w.Body.String())
	}
}
func TestRebuildFailureRestoresOriginalAndAnonymousVolume(t *testing.T) {
	var mu sync.Mutex
	old := original()
	var replacement *container.InspectResponse
	var createdHost container.HostConfig
	removedNew := false
	a, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := enginePath(q)
		switch {
		case strings.HasPrefix(p, "/images/"):
			writeJSON(w, gin.H{"Id": "sha256:old"})
		case p == "/containers/create":
			var payload struct {
				container.Config
				HostConfig container.HostConfig
			}
			if e := json.NewDecoder(q.Body).Decode(&payload); e != nil {
				t.Error(e)
			}
			createdHost = payload.HostConfig
			v := original()
			v.ID = strings.Repeat("b", 64)
			v.Name = "/app"
			v.Config = &payload.Config
			v.State.Running = false
			replacement = &v
			writeJSON(w, gin.H{"Id": v.ID, "Warnings": []string{}})
		case q.Method == "GET":
			if strings.Contains(p, old.ID) || p == "/containers/app/json" && old.Name == "/app" {
				writeJSON(w, old)
			} else if replacement != nil {
				writeJSON(w, replacement)
			} else {
				w.WriteHeader(404)
				writeJSON(w, gin.H{"message": "not found"})
			}
		case strings.HasSuffix(p, "/stop"):
			old.State.Running = false
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/rename"):
			old.Name = "/" + q.URL.Query().Get("name")
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/start"):
			if strings.Contains(p, old.ID) {
				old.State.Running = true
				w.WriteHeader(204)
			} else {
				w.WriteHeader(500)
				writeJSON(w, gin.H{"message": "simulated startup failure"})
			}
		case q.Method == "DELETE":
			if q.URL.Query().Get("v") == "1" {
				t.Error("rollback must retain volumes")
			}
			removedNew = true
			replacement = nil
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected Engine route %s %s", q.Method, p)
		}
	})
	cmd := []string{"sleep", "600"}
	ch := changes{Cmd: &cmd}
	w := request(t, r, "POST", "/api/v1/hosts/local/containers/app/config/preview", configRequest{Changes: ch})
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var preview struct{ Fingerprint string }
	if e := json.Unmarshal(w.Body.Bytes(), &preview); e != nil {
		t.Fatal(e)
	}
	w = request(t, r, "POST", "/api/v1/hosts/local/containers/app/config", configRequest{Changes: ch, Fingerprint: preview.Fingerprint, Confirmed: true})
	id := taskID(t, w)
	result := awaitTask(t, a, id)
	if result.Status != "Failed" || !strings.Contains(result.ErrorMessage, "simulated startup failure") {
		t.Fatalf("unexpected result: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if !removedNew || old.Name != "/app" || !old.State.Running {
		t.Fatalf("rollback did not restore original: name=%s running=%t removed=%t", old.Name, old.State.Running, removedNew)
	}
	if len(createdHost.Mounts) != 1 || createdHost.Mounts[0].Source != "anonymous-data" {
		t.Fatalf("anonymous volume was not preserved: %+v", createdHost.Mounts)
	}
	var cp string
	if e := a.DB.QueryRow("SELECT checkpoint FROM tasks WHERE id=?", id).Scan(&cp); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(cp, "privatevalue") || strings.Contains(cp, "SECRET") {
		t.Fatal("rebuild checkpoint leaked environment secret")
	}
}
func TestRebuildRejectsUnsupportedAndCompose(t *testing.T) {
	cases := map[string]func(*container.InspectResponse){"compose": func(v *container.InspectResponse) {
		v.Config.Labels = map[string]string{"com.docker.compose.project": "p"}
	}, "auto-remove": func(v *container.InspectResponse) { v.HostConfig.AutoRemove = true }, "shared network": func(v *container.InspectResponse) { v.HostConfig.NetworkMode = "container:other" }, "legacy links": func(v *container.InspectResponse) { v.HostConfig.Links = []string{"other:db"} }, "paused": func(v *container.InspectResponse) { v.State.Paused = true }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := original()
			mutate(&v)
			if e := validateRebuild(v); e == nil {
				t.Fatal("unsupported configuration accepted")
			}
		})
	}
}
func TestPreviewCannotApplyDifferentChanges(t *testing.T) {
	v := original()
	_, r := testApp(t, func(w http.ResponseWriter, q *http.Request) { writeJSON(w, v) })
	memory := int64(1024)
	ch := changes{Memory: &memory}
	fp := previewFingerprint(v, ch)
	memory = 2048
	w := request(t, r, "POST", "/api/v1/hosts/local/containers/app/config", configRequest{Changes: ch, Fingerprint: fp, Confirmed: true})
	if w.Code != 409 {
		t.Fatalf("changed preview accepted: %d %s", w.Code, w.Body.String())
	}
}
func TestLogsDemultiplexAndSinceReconnect(t *testing.T) {
	stamp := "2026-10-16T01:02:03.123456789Z"
	_, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(enginePath(q), "/json") {
			writeJSON(w, original())
			return
		}
		parsed, _ := time.Parse(time.RFC3339Nano, stamp)
		expected := fmt.Sprintf("%d.%09d", parsed.Unix(), parsed.Nanosecond())
		if q.URL.Query().Get("since") != expected || q.URL.Query().Get("tail") != "all" {
			t.Error("log reconnect or tail was not passed to Engine")
		}
		text := stamp + " hello from stderr\n"
		header := make([]byte, 8)
		header[0] = 2
		binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
		_, _ = w.Write(append(header, []byte(text)...))
	})
	q := httptest.NewRequest("GET", "/api/v1/hosts/local/containers/app/logs?follow=false&tail=7", nil)
	q.Header.Set("Last-Event-ID", stamp)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hello from stderr") || !strings.Contains(w.Body.String(), "id: "+stamp) {
		t.Fatalf("bad SSE: %d %s", w.Code, w.Body.String())
	}
}
func TestTerminalRejectsCrossOriginBeforeEngineAccess(t *testing.T) {
	calls := 0
	_, r := testApp(t, func(w http.ResponseWriter, q *http.Request) { calls++; w.WriteHeader(500) })
	q := httptest.NewRequest("GET", "http://manager.test/api/v1/hosts/local/containers/app/terminal?csrf_token=valid", nil)
	q.Header.Set("Origin", "https://evil.test")
	q.AddCookie(&http.Cookie{Name: "csrf_token", Value: "valid"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	if w.Code != 403 || calls != 0 {
		t.Fatalf("cross-origin terminal accessed Engine: %d, calls=%d", w.Code, calls)
	}
}
func TestCredentialEncryptionAndRegistryBinding(t *testing.T) {
	a, r := testApp(t, func(w http.ResponseWriter, q *http.Request) { t.Error("registry mismatch must not contact Engine") })
	password := "pRivate_registry_secret_123"
	w := request(t, r, "POST", "/api/v1/registry-credentials", gin.H{"name": "private", "server": "https://registry.example", "username": "alice", "password": password})
	if w.Code != 201 || strings.Contains(w.Body.String(), password) {
		t.Fatalf("credential response: %d %s", w.Code, w.Body.String())
	}
	var v credential
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	var ciphertext string
	if e := a.DB.QueryRow("SELECT secret FROM registry_credentials WHERE id=?", v.ID).Scan(&ciphertext); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(ciphertext, password) {
		t.Fatal("credential stored unencrypted")
	}
	decoded, e := a.Vault.Open(ciphertext)
	if e != nil || decoded != password {
		t.Fatal("credential encryption did not roundtrip")
	}
	w = request(t, r, "POST", "/api/v1/hosts/local/images/pull", gin.H{"image": "other.example/private:latest", "credential_id": v.ID})
	if w.Code != 400 {
		t.Fatalf("credential crossed registry boundary: %d", w.Code)
	}
}
func TestAuthorizedScanRejectsSymlinkEscape(t *testing.T) {
	a, _ := testApp(t, func(w http.ResponseWriter, q *http.Request) { t.Error("scan must not access Engine") })
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(a.Config.DataDir, "escape")); e != nil {
		t.Fatal(e)
	}
	s := &service{app: a}
	if _, e := s.resolveScanPath(filepath.Join(a.Config.DataDir, "escape", "new")); e == nil {
		t.Fatal("symlink escape accepted")
	}
	a.Config.Mode = "docker"
	a.Config.PathMappings = []config.PathMapping{{Host: "/host/data", Local: a.Config.DataDir}}
	a.Config.AllowedPaths = []string{"/host/data"}
	p, e := s.resolveScanPath("/host/data/subdir")
	canonicalDir, _ := filepath.EvalSymlinks(a.Config.DataDir)
	if e != nil || p != filepath.Join(canonicalDir, "subdir") {
		t.Fatalf("mapped path failed: %s %v", p, e)
	}
	if _, e = s.resolveScanPath("/host/data-evil"); e == nil {
		t.Fatal("prefix traversal accepted")
	}
}
func TestImageStreamFailureAndInvalidJSON(t *testing.T) {
	for _, body := range []string{`{"error":"registry denied"}`, `not-json`} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			if e := readImageProgress(strings.NewReader(body), nil); e == nil {
				t.Fatal("invalid/failed image stream accepted")
			}
		})
	}
}
func TestLimitedUploadRejectsOversize(t *testing.T) {
	r := &limitedBody{ReadCloser: io.NopCloser(strings.NewReader("1234")), remaining: 3}
	if _, e := io.ReadAll(r); e == nil {
		t.Fatal("oversized upload accepted")
	}
}

func TestInPlaceUpdateVerifiesEngineState(t *testing.T) {
	var mu sync.Mutex
	v := original()
	a, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(enginePath(q), "/update") {
			var update container.UpdateConfig
			if e := json.NewDecoder(q.Body).Decode(&update); e != nil {
				t.Error(e)
			}
			v.HostConfig.Resources = update.Resources
			v.HostConfig.RestartPolicy = update.RestartPolicy
			writeJSON(w, gin.H{"Warnings": []string{}})
			return
		}
		writeJSON(w, v)
	})
	memory := int64(256 << 20)
	ch := changes{Memory: &memory}
	w := request(t, r, "POST", "/api/v1/hosts/local/containers/app/config/preview", configRequest{Changes: ch})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var preview struct{ Fingerprint string }
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	w = request(t, r, "POST", "/api/v1/hosts/local/containers/app/config", configRequest{Changes: ch, Fingerprint: preview.Fingerprint, Confirmed: true})
	result := awaitTask(t, a, taskID(t, w))
	if result.Status != "Succeeded" {
		t.Fatalf("in-place update failed: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if v.HostConfig.Memory != memory {
		t.Fatal("actual Engine configuration unchanged")
	}
}
func TestRegistryTestDoesNotExposeIdentityToken(t *testing.T) {
	_, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		if enginePath(q) != "/auth" {
			t.Error(q.URL.Path)
		}
		var auth struct {
			Password string `json:"password"`
		}
		if e := json.NewDecoder(q.Body).Decode(&auth); e != nil {
			t.Error(e)
		}
		if auth.Password != "registry-secret" {
			t.Error("vault credential was not supplied")
		}
		writeJSON(w, gin.H{"Status": "Login Succeeded", "IdentityToken": "private-identity-token"})
	})
	w := request(t, r, "POST", "/api/v1/registry-credentials", gin.H{"name": "private", "server": "registry.example", "username": "alice", "password": "registry-secret"})
	var saved credential
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	w = request(t, r, "POST", "/api/v1/registry-credentials/"+saved.ID+"/test", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-identity-token") || !strings.Contains(w.Body.String(), "Login Succeeded") {
		t.Fatalf("registry test response: %d %s", w.Code, w.Body.String())
	}
}
func TestCrashCompensationFindsReplacementBeforeCheckpointedID(t *testing.T) {
	originalState := original()
	old := original()
	old.Name = "/app-dm-backup"
	old.State.Running = false
	replacement := original()
	replacement.ID = strings.Repeat("b", 64)
	replacement.Config.Labels = map[string]string{"io.docker-manager.rebuild-task": "crashed-task"}
	removed := false
	a, _ := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		p := enginePath(q)
		switch {
		case q.Method == "GET" && p == "/containers/app/json":
			if !removed {
				writeJSON(w, replacement)
			} else {
				w.WriteHeader(404)
				writeJSON(w, gin.H{"message": "not found"})
			}
		case q.Method == "GET":
			writeJSON(w, old)
		case q.Method == "DELETE":
			removed = true
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/rename"):
			old.Name = "/" + q.URL.Query().Get("name")
			w.WriteHeader(204)
		case strings.HasSuffix(p, "/start"):
			old.State.Running = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected: %s %s", q.Method, p)
		}
	})
	plain, _ := json.Marshal(originalState)
	sealed, e := a.Vault.Seal(string(plain))
	if e != nil {
		t.Fatal(e)
	}
	cp := rebuildCheckpoint{EncryptedOld: sealed, OriginalName: "app", BackupName: "app-dm-backup", TaskID: "crashed-task"}
	raw, _ := json.Marshal(cp)
	s := &service{app: a}
	if e = s.recoverRebuild(context.Background(), raw); e != nil {
		t.Fatal(e)
	}
	if !removed || old.Name != "/app" || !old.State.Running {
		t.Fatal("crash compensation did not restore original")
	}
}
func TestPortConflictRejectedBeforeStoppingContainer(t *testing.T) {
	stopped := false
	_, r := testApp(t, func(w http.ResponseWriter, q *http.Request) {
		p := enginePath(q)
		switch {
		case strings.HasPrefix(p, "/images/"):
			writeJSON(w, gin.H{"Id": "sha256:old"})
		case p == "/containers/json":
			writeJSON(w, []container.Summary{{ID: "other-running-container", State: "running", Ports: []container.Port{{IP: "0.0.0.0", PublicPort: 8080, PrivatePort: 80, Type: "tcp"}}}})
		case strings.HasSuffix(p, "/stop"):
			stopped = true
			w.WriteHeader(204)
		default:
			writeJSON(w, original())
		}
	})
	body := map[string]any{"changes": map[string]any{"port_bindings": map[string]any{"80/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "8080"}}}}}
	w := request(t, r, "POST", "/api/v1/hosts/local/containers/app/config/preview", body)
	if w.Code != 422 || stopped || !strings.Contains(w.Body.String(), "already used") {
		t.Fatalf("port preflight: %d %s", w.Code, w.Body.String())
	}
}

func TestRegistryHostNormalization(t *testing.T) {
	for input, want := range map[string]string{"nginx:stable": "index.docker.io", "library/nginx:stable": "index.docker.io", "docker.io/library/nginx": "index.docker.io", "registry-1.docker.io/library/nginx": "index.docker.io", "localhost:5000/nginx": "localhost:5000", "private.example/team/nginx:tag": "private.example"} {
		if got := registryHost(input); got != want {
			t.Errorf("registryHost(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestTerminalCancellationClosesWebsocketAndDockerAttach(t *testing.T) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engineClosed := make(chan struct{})
	_, router := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(enginePath(r), "/exec") {
			writeJSON(w, gin.H{"Id": "shell-exec"})
			return
		}
		if strings.HasSuffix(enginePath(r), "/start") {
			conn, rw, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = rw.Flush()
			_, _ = io.Copy(io.Discard, conn)
			close(engineClosed)
			return
		}
		t.Errorf("unexpected terminal Engine route: %s", r.URL.Path)
	}, func(c *gin.Context) { c.Request = c.Request.WithContext(sessionCtx); c.Next() })
	server := httptest.NewServer(router)
	defer server.Close()
	headers := http.Header{"Origin": []string{server.URL}, "Cookie": []string{"csrf_token=valid"}}
	ws, resp, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/hosts/local/containers/app/terminal?csrf_token=valid", headers)
	if e != nil {
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("websocket: %v %s", e, b)
		}
		t.Fatal(e)
	}
	defer ws.Close()
	cancel()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, e = ws.ReadMessage(); e == nil {
		t.Fatal("revoked terminal remained open")
	}
	select {
	case <-engineClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("Docker attachment remained open after cancellation")
	}
}

func TestQueuedScanRejectsReplacedAncestor(t *testing.T) {
	a, router := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("directory scan must not contact Engine")
	})
	parent := filepath.Join(a.Config.DataDir, "replace")
	path := filepath.Join(parent, "nested")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "nested", "private"), []byte("outside authorization"), 0600); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	_, err := a.Tasks.Submit(context.Background(), "test.block", "owned", "test", "", true, func(run *task.Run) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-run.Context().Done():
			return run.Context().Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	response := request(t, router, "POST", "/api/v1/hosts/local/storage/scan", gin.H{"path": path})
	if response.Code != http.StatusAccepted {
		t.Fatalf("scan not accepted: %d %s", response.Code, response.Body.String())
	}
	var accepted task.Task
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	result := awaitTask(t, a, accepted.ID)
	if result.Status != "Failed" {
		t.Fatalf("queued scan followed an ancestor outside authorization: %+v", result)
	}
}

func TestScanRootStaysBoundDuringAncestorSwap(t *testing.T) {
	a, _ := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("filesystem traversal must not access Engine")
	})
	parent := filepath.Join(a.Config.DataDir, "replace")
	path := filepath.Join(parent, "nested")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "owned"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &service{app: a}
	canonical, err := s.resolveScanPath(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.openScanRoot(path, canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "nested", "private"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil || len(entries) != 1 || entries[0].Name() != "owned" {
		t.Fatalf("opened directory changed after ancestor swap: entries=%v err=%v", entries, err)
	}
}

func TestHiddenTimestampsRetainReconnectCursor(t *testing.T) {
	stamp := "2026-10-16T01:02:03.123456789Z"
	_, router := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(enginePath(r), "/json") {
			writeJSON(w, original())
			return
		}
		if r.URL.Query().Get("timestamps") != "1" {
			t.Error("Engine timestamps are needed for reconnect even when hidden from display")
		}
		text := stamp + " plain log text\n"
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
		_, _ = w.Write(append(header, []byte(text)...))
	})
	response := request(t, router, "GET", "/api/v1/hosts/local/containers/app/logs?timestamps=false&follow=false", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "id: "+stamp) || !strings.Contains(response.Body.String(), `data: {"text":"plain log text"}`) {
		t.Fatalf("timestamp display toggle lost reconnect cursor or changed visible text: %d %s", response.Code, response.Body.String())
	}
}

func TestRegistryLoginRedactsEchoedCredential(t *testing.T) {
	password := "EchoFixtureCredential4XPB37"
	_, router := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, gin.H{"message": "registry refused " + password})
	})
	created := request(t, router, "POST", "/api/v1/registry-credentials", gin.H{"name": "owned", "server": "registry.example", "username": "user", "password": password})
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	var saved credential
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	response := request(t, router, "POST", "/api/v1/registry-credentials/"+saved.ID+"/test", gin.H{})
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), password) || !strings.Contains(response.Body.String(), "REDACTED") {
		t.Fatalf("registry error echoed its saved plaintext credential: %d %s", response.Code, response.Body.String())
	}
}

func TestImagePullRedactsEchoedCredentialBeforeTaskPersistence(t *testing.T) {
	password := "EchoFixturePullSecret4XPB37"
	a, router := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, gin.H{"message": "registry refused " + password})
	})
	created := request(t, router, "POST", "/api/v1/registry-credentials", gin.H{"name": "owned", "server": "registry.example", "username": "user", "password": password})
	var saved credential
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	response := request(t, router, "POST", "/api/v1/hosts/local/images/pull", gin.H{"image": "registry.example/owned:latest", "credential_id": saved.ID})
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var accepted task.Task
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	completed := awaitTask(t, a, accepted.ID)
	if completed.Status != "Failed" || strings.Contains(completed.ErrorMessage, password) || !strings.Contains(completed.ErrorMessage, "REDACTED") {
		t.Fatalf("registry transport error leaked into task: %+v", completed)
	}
	logs, err := a.Tasks.Logs(accepted.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range logs {
		if strings.Contains(log.Message, password) {
			t.Fatal("registry password leaked into persisted task log")
		}
	}
}

func TestImageProgressRedactsEchoedCredential(t *testing.T) {
	password := `echo"fixture-secret`
	body, err := json.Marshal(gin.H{"errorDetail": gin.H{"message": "registry refused " + password}})
	if err != nil {
		t.Fatal(err)
	}
	result := readImageProgress(bytes.NewReader(body), nil, password)
	if result == nil || strings.Contains(result.Error(), password) || !strings.Contains(result.Error(), "REDACTED") {
		t.Fatalf("registry stream error leaked known credential: %v", result)
	}
}

func TestPullCanceledBeforeRegistryHeadersStaysCanceled(t *testing.T) {
	started := make(chan struct{})
	a, router := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	response := request(t, router, "POST", "/api/v1/hosts/local/images/pull", gin.H{"image": "registry.example/owned:latest"})
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var accepted task.Task
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not reach its registry request")
	}
	if err := a.Tasks.Cancel(accepted.ID); err != nil {
		t.Fatal(err)
	}
	completed := awaitTask(t, a, accepted.ID)
	if completed.Status != "Canceled" || completed.ErrorCode != "CANCELED" {
		t.Fatalf("credential error sanitization changed cancellation semantics: %+v", completed)
	}
}

func TestRegistryURLCannotPersistInlineCredentials(t *testing.T) {
	a, router := testApp(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid credential URL must not contact Engine")
	})
	for _, server := range []string{"https://user:inline-fixture-secret@registry.example", "https://registry.example?token=inline-fixture-secret", "https://registry.example#inline-fixture-secret", "ftp://registry.example"} {
		response := request(t, router, "POST", "/api/v1/registry-credentials", gin.H{"name": "owned", "server": server, "username": "user", "password": "separate-secret"})
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "inline-fixture-secret") {
			t.Fatalf("inline credential URL accepted or echoed: %d %s", response.Code, response.Body.String())
		}
	}
	var count int
	if err := a.DB.QueryRow("SELECT count(*) FROM registry_credentials").Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid registry URL persisted plaintext: count=%d err=%v", count, err)
	}
}
