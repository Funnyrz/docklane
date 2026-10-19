package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/client"
)

func capabilitySettingsServer(t *testing.T, mode string, endpoint ...string) (*httptest.Server, *http.Client, *platform.App, string) {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dockerEndpoint := "unix:///unavailable-settings-test.sock"
	if len(endpoint) > 0 {
		dockerEndpoint = endpoint[0]
	}
	docker, err := client.NewClientWithOpts(client.WithHost(dockerEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	jobs := task.New(db)
	if err := jobs.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := jobs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	a := &platform.App{DB: db, Docker: docker, Tasks: jobs, Config: config.Config{Mode: mode, AllowedPaths: []string{"/original"}, ComposePath: "/missing", ResticPath: "/missing"}}
	s := httptest.NewServer(New(a, fstest.MapFS{"index.html": {Data: []byte("app")}}))
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	t.Cleanup(func() { s.Close(); cancel(); jobs.Wait(); docker.Close(); db.Close() })
	_, authState := request(t, hc, "GET", s.URL+"/api/v1/auth/status", "", "")
	status, authState := request(t, hc, "POST", s.URL+"/api/v1/auth/setup", `{"username":"admin","password":"a-strong-password-123"}`, authState["csrf_token"].(string))
	if status != 200 {
		t.Fatal(status, authState)
	}
	return s, hc, a, authState["csrf_token"].(string)
}

func capabilityPayload(paths []string, enabled bool, password string) string {
	b, _ := json.Marshal(map[string]any{"allowed_paths": paths, "path_mappings": []config.PathMapping{}, "enable_daemon_write": enabled, "password": password, "confirm": true})
	return string(b)
}

func TestCapabilitySettingsAreSavedPendingAndAppliedOnlyAtStartup(t *testing.T) {
	s, hc, a, csrf := capabilitySettingsServer(t, "binary")
	status, out := request(t, hc, "GET", s.URL+"/api/v1/settings", "", "")
	if status != 200 || out["capability_settings"].(map[string]any)["pending_restart"] != false {
		t.Fatal(status, out)
	}
	before := config.EffectiveCapabilitySettings(a.Config)
	status, out = request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", capabilityPayload([]string{"/new/../saved"}, false, "a-strong-password-123"), csrf)
	if status != 200 {
		t.Fatal(status, out)
	}
	meta := out["capability_settings"].(map[string]any)
	if meta["pending_restart"] != true || meta["saved"].(map[string]any)["allowed_paths"].([]any)[0] != "/saved" || !reflect.DeepEqual(before, config.EffectiveCapabilitySettings(a.Config)) {
		t.Fatal(out, a.Config)
	}
	got, err := config.ApplySavedCapabilitySettings(a.DB, a.Config)
	if err != nil || got.AllowedPaths[0] != "/saved" {
		t.Fatal(got, err)
	}
	var raw, result string
	if err := a.DB.QueryRow("SELECT value FROM settings WHERE key=?", config.CapabilitySettingsKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "password") || strings.Contains(raw, "confirm") {
		t.Fatal("persisted password", raw)
	}
	if err := a.DB.QueryRow("SELECT result FROM audit_logs WHERE action='settings.capabilities.update'").Scan(&result); err != nil || result != "Succeeded" {
		t.Fatal(result, err)
	}
	status, out = request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", capabilityPayload([]string{"/original"}, false, "a-strong-password-123"), csrf)
	if status != 200 || out["capability_settings"].(map[string]any)["pending_restart"] != false {
		t.Fatal(status, out)
	}
}

func TestCapabilitySettingsRejectUnauthorizedInvalidAndUnsupportedWrites(t *testing.T) {
	s, hc, a, csrf := capabilitySettingsServer(t, "docker")
	valid := capabilityPayload([]string{"/safe"}, false, "a-strong-password-123")
	for _, method := range []string{"GET", "POST"} {
		status, _ := request(t, &http.Client{}, method, s.URL+"/api/v1/settings"+map[string]string{"GET": "", "POST": "/capabilities"}[method], valid, csrf)
		if status != 401 {
			t.Fatal("anonymous accepted", status)
		}
	}
	if status, _ := request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", valid, ""); status != 403 {
		t.Fatal("missing CSRF accepted", status)
	}
	cases := []struct{ body, code string }{
		{capabilityPayload([]string{"/safe"}, false, "wrong-password"), "PASSWORD_CONFIRMATION_FAILED"},
		{capabilityPayload([]string{"/safe"}, true, "a-strong-password-123"), "SETTINGS_DAEMON_UNSUPPORTED"},
		{capabilityPayload([]string{"/"}, false, "a-strong-password-123"), "SETTINGS_INVALID_PATH"},
		{capabilityPayload([]string{"relative"}, false, "a-strong-password-123"), "SETTINGS_INVALID_PATH"},
		{capabilityPayload([]string{"/safe", "/safe/."}, false, "a-strong-password-123"), "SETTINGS_INVALID_PATH"},
		{strings.Replace(valid, `"path_mappings":[]`, `"path_mappings":[{"host":"/host","local":"/"}]`, 1), "SETTINGS_INVALID_MAPPING"},
		{strings.Replace(valid, `"confirm":true`, `"confirm":false`, 1), "INVALID_INPUT"},
		{strings.Replace(valid, `"path_mappings":[]`, `"path_mappings":null`, 1), "INVALID_INPUT"},
		{strings.Replace(valid, `"enable_daemon_write":false,`, "", 1), "INVALID_INPUT"},
		{strings.TrimSuffix(valid, "}") + `,"unknown":true}`, "INVALID_INPUT"},
		{valid + " {}", "INVALID_INPUT"},
	}
	for _, tc := range cases {
		status, out := request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", tc.body, csrf)
		if status < 400 || out["error"].(map[string]any)["code"] != tc.code {
			t.Errorf("%s: %d %#v", tc.code, status, out)
		}
	}
	var count int
	if err := a.DB.QueryRow("SELECT count(*) FROM settings WHERE key=?", config.CapabilitySettingsKey).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected input persisted", count, err)
	}
	if a.Config.AllowedPaths[0] != "/original" || a.Config.EnableDaemonWrite {
		t.Fatal("effective changed", a.Config)
	}
	// Audit insert failure must roll back the setting too.
	if _, err := a.DB.Exec("CREATE TRIGGER fail_settings_audit BEFORE INSERT ON audit_logs WHEN NEW.action='settings.capabilities.update' BEGIN SELECT RAISE(FAIL, 'audit unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	status, out := request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", valid, csrf)
	if status != 500 || out["error"].(map[string]any)["code"] != "SETTINGS_SAVE_FAILED" {
		t.Fatal(status, out)
	}
	if err := a.DB.QueryRow("SELECT count(*) FROM settings WHERE key=?", config.CapabilitySettingsKey).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial save survived rollback", count, err)
	}
}

func TestCapabilitySettingsPasswordAttemptsAreBounded(t *testing.T) {
	s, hc, a, csrf := capabilitySettingsServer(t, "binary")
	for i := 0; i < 11; i++ {
		status, out := request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", capabilityPayload([]string{}, false, "wrong-password"), csrf)
		expect := 403
		if i == 10 {
			expect = 429
		}
		if status != expect {
			t.Fatal(i, status, out)
		}
	}
	var hash string
	if err := a.DB.QueryRow("SELECT password_hash FROM users").Scan(&hash); err != nil || !security.CheckPassword(hash, "a-strong-password-123") {
		t.Fatal("password changed", err)
	}
}

func TestCapabilitySettingsDaemonGrantAndRevocationRequireRestart(t *testing.T) {
	var mutations atomic.Int64
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mutations.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer engine.Close()
	for _, mode := range []string{"binary", "docker"} {
		t.Run(mode, func(t *testing.T) {
			s, hc, a, csrf := capabilitySettingsServer(t, mode, engine.URL)
			// A real file makes accidental host-config changes observable.
			daemonFile := filepath.Join(t.TempDir(), "daemon.json")
			const original = `{"registry-mirrors":[]}`
			if err := os.WriteFile(daemonFile, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			a.Config.DaemonConfigPath = daemonFile
			status, out := request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", capabilityPayload([]string{"/original"}, true, "a-strong-password-123"), csrf)
			if runtime.GOOS == "linux" && mode == "binary" {
				if status != 200 {
					t.Fatal(status, out)
				}
				meta := out["capability_settings"].(map[string]any)
				if meta["pending_restart"] != true || meta["effective"].(map[string]any)["enable_daemon_write"] != false || meta["saved"].(map[string]any)["enable_daemon_write"] != true || a.Config.EnableDaemonWrite {
					t.Fatal("grant became effective before restart", out)
				}
				restarted, err := config.ApplySavedCapabilitySettings(a.DB, a.Config)
				if err != nil || !restarted.EnableDaemonWrite {
					t.Fatal("grant was not loaded at startup", restarted, err)
				}
				// A new process would receive this immutable effective snapshot.
				a.Config = restarted
			} else if status != 400 || out["error"].(map[string]any)["code"] != "SETTINGS_DAEMON_UNSUPPORTED" {
				t.Fatal("unsupported platform accepted a grant", status, out)
			}
			status, out = request(t, hc, "POST", s.URL+"/api/v1/settings/capabilities", capabilityPayload([]string{"/original"}, false, "a-strong-password-123"), csrf)
			if status != 200 || out["capability_settings"].(map[string]any)["saved"].(map[string]any)["enable_daemon_write"] != false {
				t.Fatal("revocation was refused", status, out)
			}
			if runtime.GOOS == "linux" && mode == "binary" && (!a.Config.EnableDaemonWrite || out["capability_settings"].(map[string]any)["pending_restart"] != true) {
				t.Fatal("revocation changed running snapshot", out)
			}
			restarted, err := config.ApplySavedCapabilitySettings(a.DB, a.Config)
			if err != nil || restarted.EnableDaemonWrite {
				t.Fatal("revocation not effective after startup", restarted, err)
			}
			content, err := os.ReadFile(daemonFile)
			if err != nil || string(content) != original || mutations.Load() != 0 {
				t.Fatal("saving settings mutated Docker or host config", string(content), mutations.Load(), err)
			}
		})
	}
}

func TestCapabilitySettingsDockerLegacyGrantDoesNotRemainPending(t *testing.T) {
	s, hc, a, _ := capabilitySettingsServer(t, "docker")
	saved := config.EffectiveCapabilitySettings(a.Config)
	saved.EnableDaemonWrite = true
	raw, _ := json.Marshal(saved)
	if _, err := a.DB.Exec("INSERT INTO settings(key,value) VALUES(?,?)", config.CapabilitySettingsKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	restarted, err := config.ApplySavedCapabilitySettings(a.DB, a.Config)
	if err != nil || restarted.EnableDaemonWrite {
		t.Fatal("legacy grant bypassed Docker startup guard", restarted, err)
	}
	a.Config = restarted
	status, out := request(t, hc, "GET", s.URL+"/api/v1/settings", "", "")
	if status != 200 {
		t.Fatal(status, out)
	}
	meta := out["capability_settings"].(map[string]any)
	if meta["pending_restart"] != false || meta["daemon_toggle_supported"] != false || meta["saved"].(map[string]any)["enable_daemon_write"] != true || meta["effective"].(map[string]any)["enable_daemon_write"] != false {
		t.Fatal("unsupported legacy grant creates permanent restart banner", out)
	}
	// Other saved changes must still be reported as pending.
	saved.AllowedPaths = []string{"/different"}
	raw, _ = json.Marshal(saved)
	if _, err := a.DB.Exec("UPDATE settings SET value=? WHERE key=?", string(raw), config.CapabilitySettingsKey); err != nil {
		t.Fatal(err)
	}
	status, out = request(t, hc, "GET", s.URL+"/api/v1/settings", "", "")
	if status != 200 || out["capability_settings"].(map[string]any)["pending_restart"] != true {
		t.Fatal("directory change no longer reports pending", status, out)
	}
}
