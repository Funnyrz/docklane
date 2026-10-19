package workflows

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/platform"
)

func daemonCapabilityService(t *testing.T) *Service {
	t.Helper()
	return &Service{app: &platform.App{Config: config.Config{Mode: "binary", EnableDaemonWrite: true, DockerEndpoint: "unix:///var/run/docker.sock", DaemonConfigPath: filepath.Join(t.TempDir(), "daemon.json")}}}
}

func requireCapabilityError(t *testing.T, err error, code, param, value string) {
	t.Helper()
	var prerequisite *CapabilityError
	if !errors.As(err, &prerequisite) || prerequisite.Code != code || prerequisite.Reason == "" || prerequisite.Error() != prerequisite.Reason {
		t.Fatalf("got %#v; want %s", err, code)
	}
	if param != "" && prerequisite.Params[param] != value {
		t.Fatalf("%s: %+v; want %s=%s", code, prerequisite.Params, param, value)
	}
}

func fakeDaemonTools(t *testing.T, names ...string) {
	t.Helper()
	tools := t.TempDir()
	for _, name := range names {
		// Executing a host tool would fail: capability detection must only locate it.
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 97\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools)
}

func TestDaemonCapabilityPreservesDeploymentGateOrdering(t *testing.T) {
	s := daemonCapabilityService(t)
	s.app.Config.Mode = "docker"
	s.app.Config.EnableDaemonWrite = false
	requireCapabilityError(t, s.daemonAllowedOn("darwin"), "HOST_OS_UNSUPPORTED", "os", "darwin")
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "HOST_MODE_UNSUPPORTED", "mode", "docker")
	s.app.Config.Mode = "binary"
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "DAEMON_WRITE_NOT_AUTHORIZED", "", "")
	s.app.Config.EnableDaemonWrite = true
	s.app.Config.DockerEndpoint = "tcp://localhost:2375"
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "LOCAL_DOCKER_ENDPOINT_REQUIRED", "", "")
	s.app.Config.DockerEndpoint = "unix:///var/run/docker.sock"
	s.app.Config.DaemonConfigPath = "relative.json"
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "DAEMON_CONFIG_PATH_INVALID", "path", "relative.json")
	// The exported report and actual operation gate must agree on this platform.
	direct, exported := s.daemonAllowed(), DaemonCapability(s.app)
	if direct == nil || exported == nil || direct.Error() != exported.Error() {
		t.Fatalf("direct %v, exported %v", direct, exported)
	}
	var got *CapabilityError
	if !errors.As(exported, &got) {
		t.Fatal(exported)
	}
	if runtime.GOOS != "linux" && got.Code != "HOST_OS_UNSUPPORTED" {
		t.Fatal(got)
	}
}

func TestDaemonCapabilityRejectsNonRegularConfigBeforeToolChecks(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s := daemonCapabilityService(t)
			path := s.app.Config.DaemonConfigPath
			if kind == "directory" {
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", t.TempDir())
			requireCapabilityError(t, s.daemonAllowedOn("linux"), "DAEMON_CONFIG_NOT_REGULAR", "path", path)
		})
	}
}

func TestDaemonCapabilityReportsMissingHostToolInOrder(t *testing.T) {
	s := daemonCapabilityService(t)
	fakeDaemonTools(t)
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "HOST_TOOL_MISSING", "tool", "dockerd")
	fakeDaemonTools(t, "dockerd")
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "HOST_TOOL_MISSING", "tool", "systemctl")
}

func TestDaemonCapabilityChecksDirectoryAndFileWritePrerequisites(t *testing.T) {
	s := daemonCapabilityService(t)
	fakeDaemonTools(t, "dockerd", "systemctl")
	parent := filepath.Join(t.TempDir(), "missing")
	s.app.Config.DaemonConfigPath = filepath.Join(parent, "daemon.json")
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "DAEMON_CONFIG_DIRECTORY_MISSING", "path", parent)
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.daemonAllowedOn("linux"); err != nil {
		t.Fatalf("missing config in writable directory should remain supported: %v", err)
	}
	path := s.app.Config.DaemonConfigPath
	if err := os.WriteFile(path, []byte("{}"), 0444); err != nil {
		t.Fatal(err)
	}
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "DAEMON_CONFIG_NOT_WRITABLE", "path", path)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.daemonAllowedOn("linux"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0755) })
	if err := s.daemonAllowedOn("linux"); err != nil {
		requireCapabilityError(t, err, "DAEMON_CONFIG_NOT_WRITABLE", "path", parent)
	} else {
		t.Log("current user can write mode-0555 directory; directory permission check cannot be exercised")
	}
}

func TestDaemonInspectionErrorsHaveStableCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&os.PathError{Op: "lstat", Path: "/private/daemon.json", Err: os.ErrPermission}, "HOST_PERMISSION_DENIED"},
		{&os.PathError{Op: "lstat", Path: "/private/daemon.json", Err: errors.New("I/O failure")}, "DAEMON_CONFIG_INSPECTION_FAILED"},
	} {
		requireCapabilityError(t, daemonInspectionError("/private/daemon.json", tc.err), tc.code, "path", "/private/daemon.json")
	}
	s := daemonCapabilityService(t)
	component := filepath.Join(t.TempDir(), "file")
	if err := os.Symlink(component, component); err != nil {
		t.Fatal(err)
	}
	s.app.Config.DaemonConfigPath = filepath.Join(component, "daemon.json")
	requireCapabilityError(t, s.daemonAllowedOn("linux"), "DAEMON_CONFIG_INSPECTION_FAILED", "path", s.app.Config.DaemonConfigPath)
}

func TestDaemonHandlersStillRejectUnauthorizedOperations(t *testing.T) {
	s := daemonCapabilityService(t)
	s.app.Config.EnableDaemonWrite = false
	for _, handler := range []struct {
		name   string
		handle gin.HandlerFunc
	}{
		{"preview", s.daemonPreview}, {"apply", s.daemonApply},
	} {
		t.Run(handler.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/host/daemon/"+handler.name, strings.NewReader(`{"confirm":true,"confirm_restart":true}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			handler.handle(ctx)
			if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "CAPABILITY_UNAVAILABLE") {
				t.Fatalf("%d: %s", recorder.Code, recorder.Body.String())
			}
			if _, err := os.Stat(s.app.Config.DaemonConfigPath); !os.IsNotExist(err) {
				t.Fatalf("unauthorized handler created configuration: %v", err)
			}
		})
	}
}
