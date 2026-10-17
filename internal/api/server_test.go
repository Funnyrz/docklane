package api

import (
	"context"
	"encoding/json"
	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/client"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func testServer(t *testing.T, endpoint ...string) (*httptest.Server, *http.Client) {
	t.Helper()
	d := t.TempDir()
	db, e := database.Open(d)
	if e != nil {
		t.Fatal(e)
	}
	v, e := security.NewVault(d)
	if e != nil {
		t.Fatal(e)
	}
	dockerEndpoint := "unix:///does-not-exist.sock"
	if len(endpoint) > 0 {
		dockerEndpoint = endpoint[0]
	}
	docker, e := client.NewClientWithOpts(client.WithHost(dockerEndpoint), client.WithAPIVersionNegotiation())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	jobs := task.New(db)
	if e = jobs.Start(ctx); e != nil {
		t.Fatal(e)
	}
	a := &platform.App{DB: db, Vault: v, Docker: docker, Tasks: jobs, Config: config.Config{DataDir: d, Mode: "binary", ComposePath: "/missing", ResticPath: "/missing"}}
	server := httptest.NewServer(New(a, fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}))
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	t.Cleanup(func() { server.Close(); cancel(); jobs.Wait(); docker.Close(); db.Close() })
	return server, hc
}

func TestDashboardUsesReadOnlyEngineState(t *testing.T) {
	var mu sync.Mutex
	mutations := 0
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			mu.Lock()
			mutations++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.51")
			w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/info"):
			io.WriteString(w, `{"Name":"fixture-host","NCPU":4,"MemTotal":8192,"Architecture":"aarch64","DockerRootDir":"/unavailable"}`)
		case strings.HasSuffix(r.URL.Path, "/version"):
			io.WriteString(w, `{"Version":"28.5.1","ApiVersion":"1.51"}`)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			io.WriteString(w, `[{"Id":"cli-created","Names":["/external"],"State":"running"},{"Id":"third-party","Names":["/stopped"],"State":"exited"}]`)
		case strings.HasSuffix(r.URL.Path, "/images/json"):
			io.WriteString(w, `[{"Id":"image"}]`)
		case strings.HasSuffix(r.URL.Path, "/volumes"):
			io.WriteString(w, `{"Volumes":[{"Name":"data"}]}`)
		case strings.HasSuffix(r.URL.Path, "/system/df"):
			io.WriteString(w, `{}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer engine.Close()
	s, hc := testServer(t, engine.URL)
	_, state := request(t, hc, "GET", s.URL+"/api/v1/auth/status", "", "")
	code, state := request(t, hc, "POST", s.URL+"/api/v1/auth/setup", `{"username":"admin","password":"a-strong-password-123"}`, state["csrf_token"].(string))
	if code != 200 {
		t.Fatal(code, state)
	}
	for i := 0; i < 2; i++ {
		code, out := request(t, hc, "GET", s.URL+"/api/v1/hosts/local/dashboard", "", "")
		if code != 200 {
			t.Fatal(code, out)
		}
		counts, ok := out["containers"].(map[string]any)
		if !ok || counts["total"] != float64(2) || counts["running"] != float64(1) || out["images"] != float64(1) || out["volumes"] != float64(1) {
			t.Fatal(out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if mutations != 0 {
		t.Fatal("refresh mutated Engine")
	}
}
func request(t *testing.T, hc *http.Client, method, url, body, csrf string) (int, map[string]any) {
	t.Helper()
	r, _ := http.NewRequest(method, url, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	resp, e := hc.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}
func TestAuthenticationCSRFAndOfflineDashboard(t *testing.T) {
	s, hc := testServer(t)
	status, _ := request(t, hc, "GET", s.URL+"/api/v1/hosts/local/dashboard", "", "")
	if status != 401 {
		t.Fatal(status)
	}
	_, out := request(t, hc, "GET", s.URL+"/api/v1/auth/status", "", "")
	csrf := out["csrf_token"].(string)
	status, _ = request(t, hc, "POST", s.URL+"/api/v1/auth/setup", `{"username":"admin","password":"a-strong-password-123"}`, "")
	if status != 403 {
		t.Fatal("csrf missing accepted", status)
	}
	status, out = request(t, hc, "POST", s.URL+"/api/v1/auth/setup", `{"username":"admin","password":"a-strong-password-123"}`, csrf)
	if status != 200 {
		t.Fatal(status, out)
	}
	csrf = out["csrf_token"].(string)
	status, _ = request(t, hc, "POST", s.URL+"/api/v1/auth/setup", `{"username":"attacker","password":"a-strong-password-123"}`, csrf)
	if status != 409 {
		t.Fatal("second setup accepted", status)
	}
	status, out = request(t, hc, "GET", s.URL+"/api/v1/hosts/local/dashboard", "", "")
	if status != 200 || out["docker"].(map[string]any)["connected"] != false || out["containers"] != nil {
		t.Fatal(status, out)
	}
	status, _ = request(t, hc, "GET", s.URL+"/api/v1/hosts/remote/dashboard", "", "")
	if status != 404 {
		t.Fatal("remote host accepted", status)
	}
}
func TestSPARoutesDoNotCaptureAPI404(t *testing.T) {
	s, hc := testServer(t)
	for _, p := range []string{"/", "/containers/abc"} {
		r, e := hc.Get(s.URL + p)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || !strings.Contains(string(b), "<html>") {
			t.Fatal(p, r.StatusCode)
		}
	}
	r, e := hc.Get(s.URL + "/api/v1/no-such-route")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if r.StatusCode != 404 || !strings.Contains(r.Header.Get("Content-Type"), "json") {
		t.Fatal("API served SPA")
	}
}
func TestConcurrentSetupCreatesOneAdministrator(t *testing.T) {
	s, hc := testServer(t)
	_, v := request(t, hc, "GET", s.URL+"/api/v1/auth/status", "", "")
	csrf := v["csrf_token"].(string)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := request(t, hc, "POST", s.URL+"/api/v1/auth/setup", `{"username":"admin","password":"a-strong-password-123"}`, csrf)
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	success := 0
	for c := range codes {
		if c == 200 {
			success++
		} else if c != 409 {
			t.Fatal(c)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
}
