package workflows

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/safepath"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
)

type Service struct {
	app       *platform.App
	scheduler *cron.Cron
	mu        sync.Mutex
	running   map[string]bool
}

var services sync.Map

func Register(r *gin.RouterGroup, app *platform.App) {
	s := &Service{app: app, running: map[string]bool{}}
	if err := s.schema(); err != nil {
		panic(fmt.Errorf("workflow schema: %w", err))
	}
	services.Store(app, s)
	s.registerCompose(r)
	s.registerBackup(r)
	s.registerHost(r)
	app.Tasks.Recover("backup", s.recoverBackup)
	app.Tasks.Recover("restore", s.recoverBackup)
	app.Tasks.Recover("daemon.configure", s.recoverDaemon)
}

// Start runs scheduling after task recovery has completed. It never catches up missed schedules.
func Start(ctx context.Context, app *platform.App) error {
	v, ok := services.Load(app)
	if !ok {
		return errors.New("workflows not registered")
	}
	s := v.(*Service)
	if err := s.recordMissedSchedules(); err != nil {
		return err
	}
	if err := s.loadSchedules(); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.Close()
				return
			case <-ticker.C:
				_, _ = s.app.DB.Exec(`INSERT INTO workflow_state(key,value) VALUES('scheduler_seen',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, now())
			}
		}
	}()
	return nil
}
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scheduler != nil {
		s.scheduler.Stop()
	}
}
func (s *Service) schema() error {
	_, err := s.app.DB.Exec(`CREATE TABLE IF NOT EXISTS workflow_state(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS compose_projects(id TEXT PRIMARY KEY,host_id TEXT NOT NULL DEFAULT 'local',name TEXT NOT NULL UNIQUE,source_type TEXT NOT NULL,workdir TEXT NOT NULL,config_paths TEXT NOT NULL,environment TEXT NOT NULL DEFAULT '{}',env_files TEXT NOT NULL DEFAULT '[]',profiles TEXT NOT NULL DEFAULT '[]',revision TEXT NOT NULL,last_deployed_at TEXT,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS backup_repositories(id TEXT PRIMARY KEY,name TEXT NOT NULL,location TEXT NOT NULL UNIQUE,encrypted_secret TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS backup_policies(id TEXT PRIMARY KEY,name TEXT NOT NULL,repository_id TEXT NOT NULL,definition TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS backup_snapshots(id TEXT PRIMARY KEY,policy_id TEXT NOT NULL,repository_id TEXT NOT NULL,snapshot_ref TEXT NOT NULL,manifest TEXT NOT NULL,status TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS schedule_instances(policy_id TEXT NOT NULL,scheduled_at_utc TEXT NOT NULL,task_id TEXT,error TEXT,PRIMARY KEY(policy_id,scheduled_at_utc));`)
	return err
}
func now() string             { return time.Now().UTC().Format(time.RFC3339Nano) }
func id() string              { return database.ID() }
func hash(b []byte) string    { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }
func bind(c *gin.Context, v any) bool {
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(v); e != nil {
		platform.Fail(c, 400, "INVALID_REQUEST", e)
		return false
	}
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		platform.Fail(c, 400, "INVALID_REQUEST", errors.New("a single JSON object within 4 MiB is required"))
		return false
	}
	return true
}
func fail(c *gin.Context, e error) {
	status := 400
	if errors.Is(e, sql.ErrNoRows) {
		status = 404
	}
	platform.Fail(c, status, "WORKFLOW_ERROR", e)
}
func (s *Service) path(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", errors.New("an absolute authorized host path is required")
	}
	if s.app.Config.Mode == "docker" {
		best := ""
		local := ""
		for _, m := range s.app.Config.PathMappings {
			rel, e := filepath.Rel(filepath.Clean(m.Host), filepath.Clean(p))
			if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(m.Host) > len(best) {
				best = m.Host
				local = filepath.Join(m.Local, rel)
			}
		}
		if best == "" {
			return "", errors.New("host path has no explicit container mapping")
		}
		roots := []string{}
		for _, m := range s.app.Config.PathMappings {
			roots = append(roots, m.Local)
		}
		return safepath.Resolve(local, roots)
	}
	return safepath.Resolve(p, s.app.Config.AllowedPaths)
}
func (s *Service) hostPath(local string) (string, error) {
	if s.app.Config.Mode != "docker" {
		return local, nil
	}
	best := ""
	host := ""
	for _, m := range s.app.Config.PathMappings {
		root, e := filepath.EvalSymlinks(m.Local)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(root, local)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(root) > len(best) {
			best = root
			host = filepath.Join(m.Host, rel)
		}
	}
	if best != "" {
		return host, nil
	}

	return "", errors.New("missing host mapping")
}

// run never invokes a shell, never logs environment or includes command output in errors.
func run(ctx context.Context, dir string, env []string, bin string, args ...string) ([]byte, error) {
	releaseThread := pinCommandThread()
	defer releaseThread()
	cmd := exec.CommandContext(ctx, bin, args...)
	configureCommand(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var b limitedBuffer
	cmd.Stdout = &b
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s canceled: %w", filepath.Base(bin), ctx.Err())
		}
		return nil, fmt.Errorf("%s failed: %w (inspect tool availability, permissions and configuration)", filepath.Base(bin), e)
	}
	if b.Overflow || stderr.Overflow {
		return nil, errors.New("tool output exceeded the 16 MiB safety limit; narrow the operation or use offline tooling")
	}
	return b.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	Overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+len(p) > 16<<20 {
		b.Overflow = true
	}
	if b.Len() < 16<<20 {
		remain := (16 << 20) - b.Len()
		if len(p) > remain {
			p = p[:remain]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func readJSONFile(p string) (map[string]any, []byte, error) {
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return map[string]any{}, nil, nil
	}
	if e != nil {
		return nil, nil, e
	}
	var v map[string]any
	e = json.Unmarshal(b, &v)
	if v == nil && e == nil {
		e = errors.New("configuration must be a JSON object")
	}
	return v, b, e
}
func decodeLines(b []byte, kind string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var last map[string]any
	for {
		var v map[string]any
		e := dec.Decode(&v)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if v["message_type"] == kind {
			last = v
		}
	}
	if last == nil {
		return nil, errors.New("tool did not return the required success record")
	}
	return last, nil
}

func (s *Service) audit(action string, handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if e := database.Audit(s.app.DB, platform.Actor(c), action, "workflow", c.Request.URL.Path, "Requested"); e != nil {
			platform.Fail(c, 500, "AUDIT_FAILED", e)
			return
		}
		handler(c)
		if c.Writer.Status() < 400 {
			if e := database.Audit(s.app.DB, platform.Actor(c), action, "workflow", c.Request.URL.Path, "Succeeded"); e != nil {
				_ = c.Error(e)
			}
		}
	}
}

type CompensationError struct{ Err error }

func (e *CompensationError) Error() string          { return "compensation requires recovery: " + e.Err.Error() }
func (e *CompensationError) Unwrap() error          { return e.Err }
func (e *CompensationError) RecoveryRequired() bool { return true }
