package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/safepath"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/image"
	"github.com/gin-gonic/gin"
)

type daemonChange struct {
	Mirrors        []string `json:"mirrors"`
	Revision       string   `json:"revision"`
	Confirm        bool     `json:"confirm"`
	ConfirmRestart bool     `json:"confirm_restart"`
}
type daemonCheckpoint struct {
	Version  int    `json:"version"`
	Path     string `json:"path"`
	Original string `json:"original"`
	Existed  bool   `json:"existed"`
	Mode     uint32 `json:"mode"`
	Written  bool   `json:"written"`
}

func (s *Service) registerHost(r *gin.RouterGroup) {
	r.POST("/host/diagnostics", s.diagnostics)
	r.GET("/host/daemon", s.daemonConfig)
	r.POST("/host/daemon/preview", s.daemonPreview)
	r.POST("/host/daemon/apply", s.daemonApply)
}
func validateMirror(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("mirror must be an http(s) origin without credentials, path, query or fragment")
	}
	return nil
}
func mergeMirrors(original []byte, mirrors []string) ([]byte, error) {
	if len(mirrors) > 32 {
		return nil, errors.New("at most 32 mirrors are allowed")
	}
	seen := map[string]bool{}
	for _, m := range mirrors {
		if e := validateMirror(m); e != nil {
			return nil, e
		}
		if seen[m] {
			return nil, errors.New("duplicate mirror")
		}
		seen[m] = true
	}
	v := map[string]json.RawMessage{}
	if len(original) > 0 {
		if e := json.Unmarshal(original, &v); e != nil {
			return nil, e
		}
		if v == nil {
			return nil, errors.New("daemon configuration must be an object")
		}
	}
	b, e := json.Marshal(mirrors)
	if e != nil {
		return nil, e
	}
	v["registry-mirrors"] = b
	return json.MarshalIndent(v, "", "  ")
}

// CapabilityError identifies an unmet prerequisite without changing the policy that
// guards host operations. Reason remains suitable for existing API consumers.
type CapabilityError struct {
	Code   string
	Params map[string]string
	Reason string
}

func (e *CapabilityError) Error() string { return e.Reason }

func (s *Service) daemonAllowed() error {
	return s.daemonAllowedOn(runtime.GOOS)
}

func (s *Service) daemonAllowedOn(goos string) error {
	deploymentReason := "daemon changes require binary mode and explicit enable_daemon_write authorization; Docker mode must use the manual host guide"
	if goos != "linux" {
		return &CapabilityError{Code: "HOST_OS_UNSUPPORTED", Params: map[string]string{"os": goos}, Reason: deploymentReason}
	}
	if s.app.Config.Mode != "binary" {
		return &CapabilityError{Code: "HOST_MODE_UNSUPPORTED", Params: map[string]string{"mode": s.app.Config.Mode}, Reason: deploymentReason}
	}
	if !s.app.Config.EnableDaemonWrite {
		return &CapabilityError{Code: "DAEMON_WRITE_NOT_AUTHORIZED", Reason: deploymentReason}
	}
	if !strings.HasPrefix(s.app.Config.DockerEndpoint, "unix://") {
		return &CapabilityError{Code: "LOCAL_DOCKER_ENDPOINT_REQUIRED", Reason: "daemon writes require a local Unix Docker endpoint"}
	}
	path := s.app.Config.DaemonConfigPath
	params := map[string]string{"path": path}
	if !filepath.IsAbs(path) {
		return &CapabilityError{Code: "DAEMON_CONFIG_PATH_INVALID", Params: params, Reason: "daemon configuration path must be absolute"}
	}
	st, e := os.Lstat(path)
	if e == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular()) {
		return &CapabilityError{Code: "DAEMON_CONFIG_NOT_REGULAR", Params: params, Reason: "daemon configuration must be a regular file, not a symlink"}
	}
	if e != nil && !os.IsNotExist(e) {
		return daemonInspectionError(path, e)
	}
	for _, tool := range []string{"dockerd", "systemctl"} {
		if _, e := exec.LookPath(tool); e != nil {
			return &CapabilityError{Code: "HOST_TOOL_MISSING", Params: map[string]string{"tool": tool}, Reason: fmt.Sprintf("required host tool %s is unavailable", tool)}
		}
	}
	parent := filepath.Dir(path)
	st, e = os.Stat(parent)
	if e != nil && !os.IsNotExist(e) {
		return daemonInspectionError(parent, e)
	}
	if e != nil || !st.IsDir() {
		return &CapabilityError{Code: "DAEMON_CONFIG_DIRECTORY_MISSING", Params: map[string]string{"path": parent}, Reason: "daemon configuration parent directory is unavailable"}
	}
	if syscall.Access(parent, 2) != nil {
		return &CapabilityError{Code: "DAEMON_CONFIG_NOT_WRITABLE", Params: map[string]string{"path": parent}, Reason: "daemon configuration directory is not writable"}
	}
	if st, e := os.Stat(path); e == nil {
		if st.Mode().Perm()&0200 == 0 {
			return &CapabilityError{Code: "DAEMON_CONFIG_NOT_WRITABLE", Params: params, Reason: "daemon configuration file is not writable"}
		}
	} else if !os.IsNotExist(e) {
		return daemonInspectionError(path, e)
	}
	return nil
}

func daemonInspectionError(path string, err error) error {
	code := "DAEMON_CONFIG_INSPECTION_FAILED"
	if errors.Is(err, os.ErrPermission) {
		code = "HOST_PERMISSION_DENIED"
	}
	return &CapabilityError{Code: code, Params: map[string]string{"path": path}, Reason: err.Error()}
}

func manualGuide() string {
	return "On the host, back up /etc/docker/daemon.json; merge registry-mirrors while retaining all other fields; run dockerd --validate --config-file /etc/docker/daemon.json; review downtime, then systemctl restart docker; verify docker info and perform an explicit pull. Restore the backup and restart if verification fails. registry-mirrors mainly applies to Docker Hub."
}
func (s *Service) daemonConfig(c *gin.Context) {
	if e := s.daemonAllowed(); e != nil {
		c.JSON(200, gin.H{"available": false, "reason": e.Error(), "manual_guide": manualGuide()})
		return
	}
	v, b, e := readJSONFile(s.app.Config.DaemonConfigPath)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"available": true, "configuration": v, "revision": hash(b), "manual_guide": manualGuide()})
}
func (s *Service) daemonPreview(c *gin.Context) {
	if e := s.daemonAllowed(); e != nil {
		platform.Fail(c, 403, "CAPABILITY_UNAVAILABLE", e)
		return
	}
	var req daemonChange
	if !bind(c, &req) {
		return
	}
	_, old, e := readJSONFile(s.app.Config.DaemonConfigPath)
	if e != nil {
		fail(c, e)
		return
	}
	next, e := mergeMirrors(old, req.Mirrors)
	if e != nil {
		fail(c, e)
		return
	}
	var value any
	_ = json.Unmarshal(next, &value)
	c.JSON(200, gin.H{"revision": hash(old), "configuration": value, "warning": "Restarting Docker can interrupt containers and the management connection. Configuration rollback does not undo application writes.", "requires_restart_confirmation": true})
}
func (s *Service) daemonApply(c *gin.Context) {
	if e := s.daemonAllowed(); e != nil {
		platform.Fail(c, 403, "CAPABILITY_UNAVAILABLE", e)
		return
	}
	var req daemonChange
	if !bind(c, &req) {
		return
	}
	if !req.Confirm || !req.ConfirmRestart {
		fail(c, errors.New("configuration and Docker restart require explicit confirmation"))
		return
	}
	if _, e := mergeMirrors(nil, req.Mirrors); e != nil {
		fail(c, e)
		return
	}
	_, old, e := readJSONFile(s.app.Config.DaemonConfigPath)
	if e != nil {
		fail(c, e)
		return
	}
	if hash(old) != req.Revision {
		fail(c, errors.New("daemon configuration changed; preview again"))
		return
	}
	platform.Submit(c, s.app, "daemon.configure", "local", false, func(t *task.Run) (err error) {
		if e := s.daemonAllowed(); e != nil {
			return e
		}
		if !s.lock("daemon") {
			return errors.New("another daemon change is active")
		}
		defer s.unlock("daemon")
		path := s.app.Config.DaemonConfigPath
		_, old, e := readJSONFile(path)
		if e != nil {
			return e
		}
		if hash(old) != req.Revision {
			return errors.New("configuration changed after confirmation")
		}
		next, e := mergeMirrors(old, req.Mirrors)
		if e != nil {
			return e
		}
		mode := os.FileMode(0644)
		existed := false
		if st, e := os.Stat(path); e == nil {
			mode = st.Mode().Perm()
			existed = true
		}
		secret, e := s.app.Vault.Seal(string(old))
		if e != nil {
			return e
		}
		cp := daemonCheckpoint{Version: 1, Path: path, Original: secret, Existed: existed, Mode: uint32(mode), Written: false}
		if e = t.Checkpoint(cp); e != nil {
			return e
		}
		defer func() {
			if err != nil {
				if recoveryErr := completeDaemonCompensation(t, &cp, s.recoverDaemon); recoveryErr != nil {
					err = &CompensationError{Err: errors.Join(err, recoveryErr)}
				}
			}
		}()
		t.Progress("validate", 10)
		if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			return e
		}
		temp, e := os.CreateTemp(filepath.Dir(path), ".dm-validate-")
		if e != nil {
			return e
		}
		tempPath := temp.Name()
		defer os.Remove(tempPath)
		if _, e = temp.Write(next); e != nil {
			temp.Close()
			return e
		}
		temp.Close()
		if _, e = run(t.Context(), "", nil, "dockerd", "--validate", "--config-file", tempPath); e != nil {
			return fmt.Errorf("dockerd configuration validation failed: %w", e)
		}
		t.Progress("backup", 25)
		if e = safepath.AtomicWrite(path+".dm-backup-"+t.ID, old, 0600); e != nil {
			return e
		}
		t.Progress("write", 40)
		cp.Written = true
		if e = t.Checkpoint(cp); e != nil {
			return e
		}
		if e = safepath.AtomicWrite(path, next, mode); e != nil {
			return e
		}
		t.Progress("restart", 55)
		if _, e = run(t.Context(), "", nil, "systemctl", "restart", "docker"); e != nil {
			return e
		}
		t.Progress("verify", 75)
		if e = s.waitDocker(t.Context()); e != nil {
			return e
		}
		info, e := s.app.Docker.Info(t.Context())
		if e != nil {
			return e
		}
		if info.RegistryConfig == nil {
			return errors.New("Docker did not report registry configuration")
		}
		for _, want := range req.Mirrors {
			found := false
			for _, got := range info.RegistryConfig.Mirrors {
				if strings.TrimRight(want, "/") == strings.TrimRight(got, "/") {
					found = true
				}
			}
			if !found {
				return errors.New("Docker daemon did not activate requested registry mirrors")
			}
		}
		complete := cp
		complete.Written = false
		if e = t.Checkpoint(complete); e != nil {
			return e
		}
		cp = complete
		return nil
	})
}
func (s *Service) waitDocker(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		ping, c := context.WithTimeout(ctx, 3*time.Second)
		_, e := s.app.Docker.Ping(ping)
		c()
		if e == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("Docker did not become available before verification deadline")
		case <-tick.C:
		}
	}
}
func (s *Service) recoverDaemon(ctx context.Context, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil
	}
	var cp daemonCheckpoint
	if e := json.Unmarshal(raw, &cp); e != nil {
		return e
	}
	if !cp.Written {
		return nil
	}
	if cp.Version != 1 || cp.Mode&^0777 != 0 {
		return errors.New("invalid daemon recovery checkpoint")
	}
	if e := s.daemonAllowed(); e != nil {
		return e
	}
	if cp.Path != s.app.Config.DaemonConfigPath {
		return errors.New("recovery path differs from authorized daemon configuration")
	}
	old, e := s.app.Vault.Open(cp.Original)
	if e != nil {
		return e
	}
	if cp.Existed {
		e = safepath.AtomicWrite(cp.Path, []byte(old), os.FileMode(cp.Mode))
	} else {
		e = os.Remove(cp.Path)
		if os.IsNotExist(e) {
			e = nil
		}
	}
	if e != nil {
		return e
	}
	if _, e = run(ctx, "", nil, "systemctl", "restart", "docker"); e != nil {
		return e
	}
	return s.waitDocker(ctx)
}
func (s *Service) diagnostics(c *gin.Context) {
	var req struct {
		URL         string `json:"url"`
		Image       string `json:"image"`
		ConfirmPull bool   `json:"confirm_pull"`
	}
	if !bind(c, &req) {
		return
	}
	if e := validateMirror(req.URL); e != nil {
		fail(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	u, _ := url.Parse(req.URL)
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, e := dialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	network := gin.H{"available": e == nil}
	if e == nil {
		conn.Close()
	} else {
		network["error"] = e.Error()
	}
	api := gin.H{"available": false}
	if e == nil {
		client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(req.URL, "/")+"/v2/", nil)
		response, err := client.Do(request)
		if err != nil {
			api["error"] = err.Error()
		} else {
			response.Body.Close()
			api["status_code"] = response.StatusCode
			api["available"] = response.StatusCode == 200 || response.StatusCode == 401
			api["authentication_required"] = response.StatusCode == 401
		}
	}
	out := gin.H{"network": network, "registry_api": api, "image_pull": gin.H{"tested": false, "reason": "An explicitly confirmed image pull is required; network/API reachability does not prove image availability."}, "manual_guide": manualGuide()}
	if req.ConfirmPull {
		if req.Image == "" || strings.HasPrefix(req.Image, "-") {
			fail(c, errors.New("image reference required for confirmed pull"))
			return
		}
		t, e := s.app.Tasks.Submit(c.Request.Context(), "mirror.diagnostic_pull", req.Image, platform.Actor(c), c.GetHeader("Idempotency-Key"), true, func(t *task.Run) error {
			stream, e := s.app.Docker.ImagePull(t.Context(), req.Image, image.PullOptions{})
			if e != nil {
				return e
			}
			defer stream.Close()
			dec := json.NewDecoder(io.LimitReader(stream, 64<<20))
			for {
				var record struct {
					Status string `json:"status"`
					Error  string `json:"error"`
				}
				e = dec.Decode(&record)
				if e == io.EOF {
					break
				}
				if e != nil {
					return e
				}
				if record.Error != "" {
					return errors.New("Engine image pull failed; review image reference, registry authentication and network configuration")
				}
				if record.Status != "" {
					t.Log(record.Status)
				}
			}
			_, e = s.app.Docker.ImageInspect(t.Context(), req.Image)
			return e
		})
		if e != nil {
			fail(c, e)
			return
		}
		out["image_pull"] = gin.H{"tested": false, "task_id": t.ID, "note": "Pull uses the Engine's current registry routing. It does not prove an unapplied mirror was used."}
	}
	c.JSON(200, out)
}

// DaemonCapability reports exactly the same policy used by mutating handlers.
func DaemonCapability(app *platform.App) error { return (&Service{app: app}).daemonAllowed() }

// completeDaemonCompensation records a completed rollback before returning the
// original failure to the task manager, whose recovery pass reads this journal.
// An unpersisted completion remains pending and must gate further mutations.
func completeDaemonCompensation(t *task.Run, cp *daemonCheckpoint, recoverFn func(context.Context, json.RawMessage) error) error {
	if !cp.Written {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := recoverFn(ctx, json.RawMessage(jsonString(*cp))); err != nil {
		return &CompensationError{Err: err}
	}
	completed := *cp
	completed.Written = false
	if err := t.Checkpoint(completed); err != nil {
		return &CompensationError{Err: fmt.Errorf("daemon rollback completed but checkpoint could not be persisted: %w", err)}
	}
	*cp = completed
	return nil
}
