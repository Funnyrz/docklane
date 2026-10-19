package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker/docker/client"
)

func capabilityApp(t *testing.T, online bool) *platform.App {
	t.Helper()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/_ping") {
			t.Errorf("unexpected Engine request %s %s", r.Method, r.URL.Path)
		}
		if !online {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("API-Version", "1.51")
		io.WriteString(w, "OK")
	}))
	t.Cleanup(engine.Close)
	docker, err := client.NewClientWithOpts(client.WithHost(engine.URL), client.WithVersion("1.51"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return &platform.App{Docker: docker, Config: config.Config{Mode: "binary", ComposePath: filepath.Join(t.TempDir(), "missing-docker"), ResticPath: filepath.Join(t.TempDir(), "missing-restic")}}
}

func capabilityTool(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCapabilitiesReportStableCodesAndToolParameters(t *testing.T) {
	app := capabilityApp(t, true)
	result := capabilities(context.Background(), app)
	for name, code := range map[string]string{"compose.manage": "DOCKER_CLI_MISSING", "backup.local": "RESTIC_MISSING", "backup.bind_mount": "ALLOWED_PATHS_MISSING"} {
		got := result[name]
		if got.Available || got.ReasonCode != code || got.Reason == "" {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if result["compose.manage"].ReasonParams["tool"] != app.Config.ComposePath || result["backup.local"].ReasonParams["tool"] != app.Config.ResticPath {
		t.Fatal(result)
	}
	if !result["docker.manage"].Available {
		t.Fatal(result)
	}
	expectedDaemon := "DAEMON_WRITE_NOT_AUTHORIZED"
	if runtime.GOOS != "linux" {
		expectedDaemon = "HOST_OS_UNSUPPORTED"
	}
	if result["host.daemon_config"].ReasonCode != expectedDaemon || result["host.service_restart"].ReasonCode != expectedDaemon {
		t.Fatal(result)
	}
}

func TestComposeCapabilityDistinguishesVersionAndEngineFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		online     bool
		code       string
	}{
		{"v2", "echo v2.39.1", true, ""},
		{"old version", "echo 1.29.2", true, "COMPOSE_V2_UNAVAILABLE"},
		{"no plugin", "exit 1", true, "COMPOSE_V2_UNAVAILABLE"},
		{"engine unavailable", "echo 2.39.1", false, "DOCKER_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := capabilityApp(t, tc.online)
			// This temporary tool rejects every command except the read-only plugin query.
			app.Config.ComposePath = capabilityTool(t, "[ \"$*\" = \"compose version --short\" ] || exit 42\n"+tc.body)
			got := capabilities(context.Background(), app)["compose.manage"]
			if got.ReasonCode != tc.code || got.Available != (tc.code == "") {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestBindCapabilityChecksAllowedPathsBeforeContainerMappings(t *testing.T) {
	app := capabilityApp(t, true)
	app.Config.Mode = "docker"
	if got := capabilities(context.Background(), app)["backup.bind_mount"]; got.ReasonCode != "ALLOWED_PATHS_MISSING" {
		t.Fatal(got)
	}
	app.Config.AllowedPaths = []string{"/srv/data"}
	if got := capabilities(context.Background(), app)["backup.bind_mount"]; got.ReasonCode != "PATH_MAPPINGS_MISSING" {
		t.Fatal(got)
	}
	app.Config.PathMappings = []config.PathMapping{{Host: "/srv/data", Local: "/data"}}
	if got := capabilities(context.Background(), app)["backup.bind_mount"]; !got.Available || got.ReasonCode != "" || got.Reason != "" {
		t.Fatal(got)
	}
	app.Config.Mode = "binary"
	app.Config.PathMappings = nil
	if got := capabilities(context.Background(), app)["backup.bind_mount"]; !got.Available {
		t.Fatal(got)
	}
}

func TestCapabilitySerializationKeepsLegacyFieldsAndOmitsAvailableReasons(t *testing.T) {
	for _, tc := range []struct {
		cap  capability
		want string
	}{
		{capability{Available: true}, `{"available":true,"reason":""}`},
		{capability{Reason: "Restic is not installed", ReasonCode: "RESTIC_MISSING", ReasonParams: map[string]string{"tool": "/tools/restic"}}, `{"available":false,"reason":"Restic is not installed","reason_code":"RESTIC_MISSING","reason_params":{"tool":"/tools/restic"}}`},
	} {
		got, err := json.Marshal(tc.cap)
		if err != nil || string(got) != tc.want {
			t.Fatalf("%s, %v; want %s", got, err, tc.want)
		}
	}
	app := capabilityApp(t, true)
	app.Config.ResticPath = capabilityTool(t, "exit 42")
	if got := capabilities(context.Background(), app)["backup.local"]; !got.Available || got.Reason != "" || got.ReasonCode != "" || got.ReasonParams != nil {
		t.Fatal(got)
	}
}
