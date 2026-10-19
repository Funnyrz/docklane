package workflows

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/safepath"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/errdefs"
	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
)

type Repository struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
	Status   string `json:"status"`
	secret   string
}
type Target struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}
type Policy struct {
	AllowedWriters []string `json:"allowed_writers"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	HostID         string   `json:"host_id"`
	RepositoryID   string   `json:"repository_id"`
	Targets        []Target `json:"targets"`
	Consistency    string   `json:"consistency_mode"`
	Schedule       string   `json:"schedule"`
	Timezone       string   `json:"timezone"`
	KeepLast       int      `json:"keep_last"`
	Excludes       []string `json:"excludes"`
	Retries        int      `json:"retries"`
	RetrySeconds   int      `json:"retry_seconds"`
	Enabled        bool     `json:"enabled"`
}
type backupSource struct {
	HostPath     string `json:"host_path,omitempty"`
	LocalPath    string `json:"local_path,omitempty"`
	Volume       string `json:"volume,omitempty"`
	SnapshotPath string `json:"snapshot_path"`
}
type Manifest struct {
	Version            int                        `json:"version"`
	CreatedAt          string                     `json:"created_at"`
	Policy             Policy                     `json:"policy"`
	Sources            []backupSource             `json:"sources"`
	Containers         []json.RawMessage          `json:"containers"`
	Compose            []Project                  `json:"compose"`
	ComposeConfig      map[string]json.RawMessage `json:"compose_config,omitempty"`
	ConsistencyWarning string                     `json:"consistency_warning,omitempty"`
}
type stopCheckpoint struct {
	HoldWriters  bool     `json:"hold_writers,omitempty"`
	Version      int      `json:"version"`
	Running      []string `json:"running"`
	Worker       string   `json:"worker,omitempty"`
	Created      []string `json:"created,omitempty"`
	RecoveryTask string   `json:"recovery_task,omitempty"`
	Stage        string   `json:"stage,omitempty"`
}

func (s *Service) registerBackup(r *gin.RouterGroup) {
	r.GET("/backup/repositories", s.repositories)
	r.POST("/backup/repositories", s.audit("createRepository", s.createRepository))
	r.POST("/backup/repositories/:id/export", s.audit("exportRepository", s.exportRepository))
	r.GET("/backup/repositories/:id/snapshots", s.snapshots)
	r.GET("/backup/repositories/:id/snapshots/:snapshot/metadata", s.snapshotMetadata)
	r.GET("/backup/policies", s.policies)
	r.POST("/backup/policies", s.audit("createPolicy", s.createPolicy))
	r.DELETE("/backup/policies/:id", s.audit("deletePolicy", s.deletePolicy))
	r.PUT("/backup/policies/:id", s.audit("updatePolicy", s.updatePolicy))
	r.POST("/backup/policies/:id/run", s.audit("runPolicy", s.runPolicy))
	r.POST("/backup/policies/:id/preview", s.audit("previewBackup", s.previewBackup))
	r.GET("/backup/schedule-instances", s.scheduleInstances)
	r.POST("/backup/restore/preview", s.audit("previewRestore", s.previewRestore))
	r.POST("/backup/restore", s.audit("restore", s.restore))
}
func (s *Service) getRepository(id string) (Repository, error) {
	var r Repository
	e := s.app.DB.QueryRow(`SELECT id,name,location,status,encrypted_secret FROM backup_repositories WHERE id=?`, id).Scan(&r.ID, &r.Name, &r.Location, &r.Status, &r.secret)
	return r, e
}
func (s *Service) repositories(c *gin.Context) {
	rows, e := s.app.DB.Query(`SELECT id,name,location,status FROM backup_repositories ORDER BY created_at DESC`)
	if e != nil {
		fail(c, e)
		return
	}
	defer rows.Close()
	out := []Repository{}
	for rows.Next() {
		var r Repository
		if e = rows.Scan(&r.ID, &r.Name, &r.Location, &r.Status); e != nil {
			fail(c, e)
			return
		}
		out = append(out, r)
	}
	c.JSON(200, out)
}
func (s *Service) restic(ctx context.Context, r Repository, args ...string) ([]byte, error) {
	p, e := s.path(r.Location)
	if e != nil {
		return nil, e
	}
	secret, e := s.app.Vault.Open(r.secret)
	if e != nil {
		return nil, e
	}
	return run(ctx, "", []string{"RESTIC_PASSWORD=" + secret, "RESTIC_REPOSITORY=" + p}, s.app.Config.ResticPath, args...)
}
func (s *Service) createRepository(c *gin.Context) {
	var req struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		Password string `json:"password"`
		Import   bool   `json:"import"`
	}
	if !bind(c, &req) {
		return
	}
	if (!req.Import && len(req.Password) < 12) || (req.Import && req.Password == "") || req.Name == "" {
		fail(c, errors.New("name required; a new repository password needs at least 12 characters, import requires the existing password"))
		return
	}
	p, e := s.path(req.Location)
	if e != nil {
		fail(c, e)
		return
	}
	if req.Location == "/" {
		fail(c, errors.New("repository cannot be filesystem root"))
		return
	}
	if !req.Import {
		if e = os.MkdirAll(p, 0700); e != nil {
			fail(c, e)
			return
		}
		entries, e := os.ReadDir(p)
		if e != nil || len(entries) != 0 {
			fail(c, errors.New("new repository directory must be empty"))
			return
		}
	}
	secret, e := s.app.Vault.Seal(req.Password)
	if e != nil {
		fail(c, e)
		return
	}
	canonical, e := s.hostPath(p)
	if e != nil {
		fail(c, e)
		return
	}
	r := Repository{ID: id(), Name: req.Name, Location: canonical, Status: "ready", secret: secret}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	args := []string{"init"}
	if req.Import {
		args = []string{"snapshots", "--json"}
	}
	if _, e = s.restic(ctx, r, args...); e != nil {
		fail(c, e)
		return
	}
	_, e = s.app.DB.Exec(`INSERT INTO backup_repositories(id,name,location,encrypted_secret,status,created_at) VALUES(?,?,?,?,?,?)`, r.ID, r.Name, r.Location, r.secret, r.Status, now())
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, r)
}
func (s *Service) exportRepository(c *gin.Context) {
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if !bind(c, &req) {
		return
	}
	if !req.Confirm {
		fail(c, errors.New("confirm offline recovery secret export"))
		return
	}
	r, e := s.getRepository(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	secret, e := s.app.Vault.Open(r.secret)
	if e != nil {
		fail(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", "attachment; filename=restic-recovery.json")
	c.JSON(200, gin.H{"format_version": 1, "location": r.Location, "password": secret, "recovery": "Import this location and password into a new installation. The original SQLite database and application key are not required. Keep this file offline."})
}
func (s *Service) snapshots(c *gin.Context) {
	r, e := s.getRepository(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
	defer cancel()
	b, e := s.restic(ctx, r, "snapshots", "--json")
	if e != nil {
		fail(c, e)
		return
	}
	var v []map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		fail(c, e)
		return
	}
	for _, row := range v {
		var tags []string
		encoded, err := json.Marshal(row["tags"])
		if err != nil {
			fail(c, err)
			return
		}
		if err = json.Unmarshal(encoded, &tags); err != nil {
			fail(c, err)
			return
		}
		row["verification_status"], row["verification_warning"] = snapshotVerification(tags)
	}
	c.JSON(200, v)
}
func validatePolicy(p Policy) error {
	if p.Name == "" || p.RepositoryID == "" || len(p.Targets) == 0 {
		return errors.New("name, repository and targets required")
	}
	if p.HostID != "" && p.HostID != "local" {
		return errors.New("only local host is available")
	}
	if p.Consistency != "file" && p.Consistency != "stop" {
		return errors.New("consistency_mode must be file or stop; logical database export is unavailable")
	}
	if p.KeepLast < 1 || p.KeepLast > 10000 {
		return errors.New("keep_last must be between 1 and 10000")
	}
	if p.Retries < 0 || p.Retries > 5 || p.RetrySeconds < 0 || p.RetrySeconds > 3600 {
		return errors.New("invalid retry limits")
	}
	if p.Timezone == "" {
		return errors.New("IANA timezone required")
	}
	if _, e := time.LoadLocation(p.Timezone); e != nil {
		return e
	}
	if p.Schedule != "" {
		_, e := cron.ParseStandard("CRON_TZ=" + p.Timezone + " " + p.Schedule)
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) getPolicy(id string) (Policy, error) {
	var raw string
	var p Policy
	e := s.app.DB.QueryRow(`SELECT definition FROM backup_policies WHERE id=?`, id).Scan(&raw)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &p)
	}
	return p, e
}
func (s *Service) policies(c *gin.Context) {
	rows, e := s.app.DB.Query(`SELECT definition FROM backup_policies ORDER BY created_at DESC`)
	if e != nil {
		fail(c, e)
		return
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			fail(c, e)
			return
		}
		var p Policy
		if e = json.Unmarshal([]byte(raw), &p); e != nil {
			fail(c, e)
			return
		}
		out = append(out, p)
	}
	c.JSON(200, out)
}
func (s *Service) createPolicy(c *gin.Context) {
	var p Policy
	if !bind(c, &p) {
		return
	}
	p.ID = id()
	p.HostID = "local"
	if e := validatePolicy(p); e != nil {
		fail(c, e)
		return
	}
	r, e := s.getRepository(p.RepositoryID)
	if e != nil {
		fail(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
	defer cancel()
	sources, _, _, e := s.sources(ctx, p)
	if e == nil {
		e = s.validateSources(r, sources)
	}
	if e != nil {
		fail(c, e)
		return
	}
	if p.Consistency == "stop" {
		p.AllowedWriters, e = s.writers(ctx, sources)
		if e != nil {
			fail(c, e)
			return
		}
		sort.Strings(p.AllowedWriters)
	}
	_, e = s.app.DB.Exec(`INSERT INTO backup_policies(id,name,repository_id,definition,created_at) VALUES(?,?,?,?,?)`, p.ID, p.Name, p.RepositoryID, jsonString(p), now())
	if e != nil {
		fail(c, e)
		return
	}
	if e = s.loadSchedules(); e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, p)
}
func (s *Service) deletePolicy(c *gin.Context) {
	_, e := s.app.DB.Exec(`DELETE FROM backup_policies WHERE id=?`, c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	if e = s.loadSchedules(); e != nil {
		fail(c, e)
		return
	}
	c.Status(204)
}
func (s *Service) loadSchedules() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scheduler != nil {
		s.scheduler.Stop()
	}
	cr := cron.New()
	rows, e := s.app.DB.Query(`SELECT definition FROM backup_policies`)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return e
		}
		var p Policy
		if e = json.Unmarshal([]byte(raw), &p); e != nil {
			return e
		}
		if !p.Enabled || p.Schedule == "" {
			continue
		}
		var entryID cron.EntryID
		entryID, e = cr.AddFunc("CRON_TZ="+p.Timezone+" "+p.Schedule, func() {
			at := cr.Entry(entryID).Prev.UTC().Format(time.RFC3339)
			res, e := s.app.DB.Exec(`INSERT OR IGNORE INTO schedule_instances(policy_id,scheduled_at_utc) VALUES(?,?)`, p.ID, at)
			if e != nil {
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				return
			}
			t, e := s.app.Tasks.Submit(context.Background(), "backup", p.ID, "scheduler", "schedule:"+p.ID+":"+at, true, func(t *task.Run) error { return s.backup(t, p) })
			if e != nil {
				_, _ = s.app.DB.Exec(`UPDATE schedule_instances SET error=? WHERE policy_id=? AND scheduled_at_utc=?`, e.Error(), p.ID, at)
				return
			}
			_, _ = s.app.DB.Exec(`UPDATE schedule_instances SET task_id=? WHERE policy_id=? AND scheduled_at_utc=?`, t.ID, p.ID, at)
		})
		if e != nil {
			return e
		}
	}
	s.scheduler = cr
	cr.Start()
	return nil
}
func (s *Service) runPolicy(c *gin.Context) {
	p, e := s.getPolicy(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	var req struct {
		Confirm  bool   `json:"confirm"`
		Revision string `json:"revision"`
	}
	if !bind(c, &req) {
		return
	}
	if !req.Confirm {
		fail(c, errors.New("confirm backup and consistency risks"))
		return
	}
	approvedSources, approvedWriters, revision, e := s.backupPreflight(c.Request.Context(), p)
	if e != nil {
		fail(c, e)
		return
	}
	if p.Consistency == "stop" && req.Revision != revision {
		fail(c, errors.New("preview and confirm the complete writer stop scope before running"))
		return
	}
	platform.Submit(c, s.app, "backup", p.ID, true, func(t *task.Run) error {
		if p.Consistency == "stop" {
			_, _, current, e := s.backupPreflight(t.Context(), p)
			if e != nil {
				return e
			}
			if current != revision {
				return errors.New("backup writer scope changed after confirmation")
			}
		}
		return s.backup(t, p, backupApproval{Sources: approvedSources, Writers: approvedWriters})
	})
}
func (s *Service) sources(ctx context.Context, p Policy) ([]backupSource, []json.RawMessage, []Project, error) {
	sources := []backupSource{}
	inspects := []json.RawMessage{}
	projects := []Project{}
	seen := map[string]bool{}
	var addContainer func(string) error
	addPath := func(path string) error {
		q, e := s.path(path)
		if e != nil {
			return e
		}
		st, e := os.Stat(q)
		if e != nil || !st.IsDir() && !st.Mode().IsRegular() {
			return errors.New("backup source must be an existing directory or regular file")
		}
		for _, existing := range sources {
			if existing.LocalPath != "" {
				if existingInfo, statErr := os.Stat(existing.LocalPath); statErr == nil && existingInfo.IsDir() && (q == existing.LocalPath || strings.HasPrefix(q, existing.LocalPath+string(filepath.Separator))) {
					return nil
				}
			}
		}
		if !seen["path:"+q] {
			host, err := s.hostPath(q)
			if err != nil {
				return err
			}
			sources = append(sources, backupSource{HostPath: host, LocalPath: q})
			seen["path:"+q] = true
		}
		return nil
	}
	addVolume := func(name string) error {
		v, e := s.app.Docker.VolumeInspect(ctx, name)
		if e != nil {
			return e
		}
		if v.Driver != "local" || len(v.Options) > 0 {
			return errors.New("only local volumes without driver options can be backed up")
		}
		if !seen["volume:"+name] {
			sources = append(sources, backupSource{Volume: name, HostPath: v.Mountpoint})
			seen["volume:"+name] = true
		}
		return nil
	}
	addContainer = func(name string) error {
		ct, e := s.app.Docker.ContainerInspect(ctx, name)
		if e != nil {
			return e
		}
		b, e := json.Marshal(ct)
		if e != nil {
			return e
		}
		inspects = append(inspects, b)
		for _, m := range ct.Mounts {
			if m.Type == mount.TypeVolume {
				if e = addVolume(m.Name); e != nil {
					return e
				}
			} else if m.Type == mount.TypeBind {
				if e = addPath(m.Source); e != nil {
					return e
				}
			}
		}
		return nil
	}
	for _, target := range p.Targets {
		var e error
		switch target.Type {
		case "directory":
			e = addPath(target.Path)
		case "volume":
			e = addVolume(target.Name)
		case "container":
			e = addContainer(target.Name)
		case "compose":
			var project Project
			project, e = s.getProject(target.Name)
			if e == nil {
				projects = append(projects, project)
				if e = addPath(project.Workdir); e != nil {
					return nil, nil, nil, e
				}
				dependencies, dependencyErr := s.composeDependencies(ctx, project)
				if dependencyErr != nil {
					return nil, nil, nil, dependencyErr
				}
				for _, dependency := range dependencies {
					if e = addPath(dependency); e != nil {
						return nil, nil, nil, e
					}
				}
				var cs []container.Summary
				cs, e = s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
				if e == nil {
					for _, ct := range cs {
						if ct.Labels["com.docker.compose.project"] == project.Name {
							if e = addContainer(ct.ID); e != nil {
								break
							}
						}
					}
				}
			}
		default:
			e = errors.New("unknown backup target type")
		}
		if e != nil {
			return nil, nil, nil, e
		}
	}
	if len(sources) == 0 {
		return nil, nil, nil, errors.New("no file sources found for backup")
	}
	for i := range sources {
		sources[i].SnapshotPath = fmt.Sprintf("/sources/%03d", i)
	}
	return sources, inspects, projects, nil
}
func (s *Service) validateSources(r Repository, sources []backupSource) error {
	repo, e := s.path(r.Location)
	if e != nil {
		return e
	}
	if _, e = os.Stat(repo); e != nil {
		return e
	}
	for _, src := range sources {
		if src.LocalPath != "" {
			if e = safepath.Disjoint(src.LocalPath, repo); e != nil {
				return e
			}
		}
		if src.HostPath != "" {
			if src.HostPath == "/" || overlaps(src.HostPath, r.Location) {
				return errors.New("backup source and repository cannot contain one another or be host root")
			}
			if src.Volume == "" {
				if e = safepath.Disjoint(src.HostPath, r.Location); e != nil {
					return e
				}
			}
		}
	}
	return checkFree(repo)
}
func (s *Service) writers(ctx context.Context, sources []backupSource) ([]string, error) {
	cts, e := s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, ct := range cts {
		if ct.State != "running" {
			continue
		}
		for _, m := range ct.Mounts {
			if !m.RW {
				continue
			}
			found := false
			for _, src := range sources {
				if src.Volume != "" && m.Type == mount.TypeVolume && m.Name == src.Volume {
					found = true
				}
				if src.HostPath != "" && m.Source != "" && overlaps(src.HostPath, m.Source) {
					found = true
				}
			}
			if found {
				out = append(out, ct.ID)
				break
			}
		}
	}
	if e := s.guardStopWriters(ctx, out); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *Service) recoverBackup(ctx context.Context, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var cp stopCheckpoint
	if e := json.Unmarshal(raw, &cp); e != nil {
		return e
	}
	if len(cp.Running) == 0 && cp.Worker == "" && len(cp.Created) == 0 && cp.RecoveryTask == "" && cp.Stage == "" {
		return nil
	}
	if cp.Version != 1 {
		return errors.New("unsupported backup recovery checkpoint version")
	}
	for _, v := range append(append(append([]string{}, cp.Running...), cp.Created...), cp.Worker) {
		if v != "" && !validDockerID(v) {
			return errors.New("invalid recovery container ID")
		}
	}
	if cp.Worker != "" {
		if e := s.app.Docker.ContainerRemove(ctx, cp.Worker, container.RemoveOptions{Force: true}); e != nil && !errdefs.IsNotFound(e) {
			return fmt.Errorf("backup worker remains active; original writers stay stopped until it can be removed: %w", e)
		}
	}
	var errs []error
	if cp.RecoveryTask != "" {
		all, e := s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
		if e != nil {
			errs = append(errs, e)
		} else {
			for _, ct := range all {
				if ct.Labels["docker-manager.recovery"] == cp.RecoveryTask {
					if e = s.app.Docker.ContainerRemove(ctx, ct.ID, container.RemoveOptions{Force: true, RemoveVolumes: false}); e != nil && !errdefs.IsNotFound(e) {
						errs = append(errs, e)
					}
				}
			}
		}
	}
	for _, id := range cp.Created {
		if e := s.app.Docker.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false}); e != nil && !errdefs.IsNotFound(e) {
			errs = append(errs, e)
		}
	}
	if cp.Stage != "" {
		cleanupErr := func() error {
			stage, e := s.path(cp.Stage)
			if e != nil {
				return e
			}
			if !strings.HasPrefix(filepath.Base(stage), ".dm-manifest-") {
				return errors.New("invalid backup staging recovery path")
			}
			rows, e := s.app.DB.Query(`SELECT location FROM backup_repositories`)
			if e != nil {
				return e
			}
			owned := false
			for rows.Next() {
				var location string
				if e = rows.Scan(&location); e != nil {
					rows.Close()
					return e
				}
				repo, e := s.path(location)
				if e == nil && filepath.Dir(stage) == repo {
					owned = true
				}
			}
			rows.Close()
			if !owned {
				return errors.New("backup staging recovery path does not belong to a configured repository")
			}
			return os.RemoveAll(stage)
		}()
		if cleanupErr != nil {
			errs = append(errs, cleanupErr)
		}
	}
	if cp.HoldWriters {
		return errors.Join(errs...)
	}
	for _, id := range cp.Running {
		ct, e := s.app.Docker.ContainerInspect(ctx, id)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		if ct.State != nil && !ct.State.Running {
			if e = s.app.Docker.ContainerStart(ctx, id, container.StartOptions{}); e != nil {
				errs = append(errs, e)
			}
		}
	}
	return errors.Join(errs...)
}
func (s *Service) lock(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[key] {
		return false
	}
	s.running[key] = true
	return true
}
func (s *Service) unlock(key string) { s.mu.Lock(); delete(s.running, key); s.mu.Unlock() }

type backupApproval struct {
	Sources []backupSource
	Writers []string
}

func (s *Service) backup(t *task.Run, p Policy, approval ...backupApproval) (err error) {
	if !s.lock("repo:" + p.RepositoryID) {
		return errors.New("repository already has an active operation")
	}
	defer s.unlock("repo:" + p.RepositoryID)
	r, e := s.getRepository(p.RepositoryID)
	if e != nil {
		return e
	}
	t.Progress("preflight", 5)
	sources, cts, projects, e := s.sources(t.Context(), p)
	if e != nil {
		return e
	}
	if e = s.validateSources(r, sources); e != nil {
		return e
	}
	unmodifiedSources := append([]backupSource{}, sources...)
	worker := backupNeedsWorker(s.app.Config.Mode, sources)
	if worker {
		if e = s.prepareWorker(t); e != nil {
			return e
		}
		// Preparing an image may take minutes. Re-read the sources before
		// approving any writer stop against the originally confirmed scope.
		sources, cts, projects, e = s.sources(t.Context(), p)
		if e != nil {
			return e
		}
		if jsonString(sources) != jsonString(unmodifiedSources) {
			return errors.New("backup sources changed while preparing the worker image; preview again")
		}
		if e = s.validateSources(r, sources); e != nil {
			return e
		}
	}
	if !worker {
		for i := range sources {
			sources[i].SnapshotPath = sources[i].LocalPath
		}
	}
	composeConfigs := map[string]json.RawMessage{}
	for _, project := range projects {
		b, e := s.compose(t.Context(), project, "config", "--format", "json")
		if e != nil {
			return e
		}
		composeConfigs[project.ID] = b
	}
	manifest := Manifest{ComposeConfig: composeConfigs, Version: 1, CreatedAt: now(), Policy: p, Sources: sources, Containers: cts, Compose: projects}
	if p.Consistency == "file" {
		manifest.ConsistencyWarning = "Live file backup is not a consistent database backup. Stop writers or use application-aware export before relying on it."
		t.Log(manifest.ConsistencyWarning)
	}
	cp := stopCheckpoint{Version: 1}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		restoreErr := s.recoverBackup(ctx, []byte(jsonString(cp)))
		if restoreErr != nil {
			err = &CompensationError{Err: errors.Join(err, fmt.Errorf("restore original runtime state: %w", restoreErr))}
		}
	}()
	if p.Consistency == "stop" {
		cp.Running, e = s.writers(t.Context(), sources)
		if e != nil {
			return e
		}
		sort.Strings(cp.Running)
		if len(approval) > 0 {
			expected := append([]string{}, approval[0].Writers...)
			sort.Strings(expected)
			if jsonString(expected) != jsonString(cp.Running) || jsonString(unmodifiedSources) != jsonString(approval[0].Sources) {
				cp.Running = nil
				return errors.New("backup sources or exact writer stop scope changed after confirmation; preview again")
			}
		} else {
			allowed := map[string]bool{}
			for _, writer := range p.AllowedWriters {
				allowed[writer] = true
			}
			for _, writer := range cp.Running {
				if !allowed[writer] {
					cp.Running = nil
					return errors.New("scheduled backup found a writer outside the policy's approved stop scope; update the policy before running")
				}
			}
		}
		if e = t.Checkpoint(cp); e != nil {
			return e
		}
		t.Progress("stop_writers", 10)
		for _, id := range cp.Running {
			if e = s.app.Docker.ContainerStop(t.Context(), id, container.StopOptions{}); e != nil {
				return e
			}
		}
		remaining, e := s.writers(t.Context(), sources)
		if e != nil {
			return e
		}
		if len(remaining) > 0 {
			return errors.New("new writers appeared after stop; backup refused")
		}
	}
	repoLocal, e := s.path(r.Location)
	if e != nil {
		return e
	}
	stage, e := os.MkdirTemp(repoLocal, ".dm-manifest-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	cp.Stage, e = s.hostPath(stage)
	if e != nil {
		return e
	}
	if e = t.Checkpoint(cp); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(stage, "manifest.json"), []byte(jsonString(manifest)), 0600); e != nil {
		return e
	}
	secret, e := s.app.Vault.Open(r.secret)
	if e != nil {
		return e
	}
	t.Progress("backup", 25)
	var b []byte
	for attempt := 0; attempt <= p.Retries; attempt++ {
		t.Retry(attempt)
		b, e = s.backupFiles(t, r, sources, stage, secret, &cp, p)
		if e == nil {
			break
		}
		if attempt < p.Retries {
			t.Log("Backup tool failed; retrying after configured interval")
			select {
			case <-t.Context().Done():
				return t.Context().Err()
			case <-time.After(time.Duration(p.RetrySeconds) * time.Second):
			}
		}
	}
	if e != nil {
		return e
	}
	summary, e := decodeLines(b, "summary")
	if e != nil {
		return e
	}
	ref, _ := summary["snapshot_id"].(string)
	if ref == "" {
		return errors.New("backup did not commit a snapshot")
	}
	t.Progress("verify_manifest", 60)
	committed, _, manifestErr := s.readManifest(t.Context(), r, ref)
	if manifestErr != nil {
		return fmt.Errorf("snapshot committed but recovery manifest is missing or unreadable; no retention cleanup performed: %w", manifestErr)
	}
	if hash([]byte(jsonString(committed))) != hash([]byte(jsonString(manifest))) {
		return errors.New("committed recovery manifest differs from expected manifest; no retention cleanup performed")
	}
	t.Progress("read_data_verification", 70)
	if _, e = s.restic(t.Context(), r, "check", "--read-data"); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	e = s.recoverBackup(ctx, []byte(jsonString(cp)))
	cancel()
	if e != nil {
		return fmt.Errorf("backup complete but writer recovery failed: %w", e)
	}
	cp = stopCheckpoint{Version: 1}
	if e = t.Checkpoint(cp); e != nil {
		return e
	}
	t.Progress("commit_verification", 85)
	ref, e = s.markSnapshotVerified(t.Context(), r, ref)
	if e != nil {
		return fmt.Errorf("snapshot data verified but repository completion marker failed; no retention cleanup performed: %w", e)
	}
	t.Log("Verified snapshot committed: " + ref)
	_, e = s.app.DB.Exec(`INSERT INTO backup_snapshots(id,policy_id,repository_id,snapshot_ref,manifest,status,created_at) VALUES(?,?,?,?,?,'verified',?)`, id(), p.ID, p.RepositoryID, ref, jsonString(map[string]any{"version": 1, "policy_id": p.ID, "sources": sources}), now())
	if e != nil {
		return e
	}
	t.Progress("retention", 90)
	_, e = s.restic(t.Context(), r, "forget", "--tag", "dm-policy:"+p.ID+","+verifiedSnapshotTag, "--group-by", "tags", "--keep-last", fmt.Sprint(p.KeepLast), "--prune")
	return e
}
func (s *Service) backupFiles(t *task.Run, r Repository, sources []backupSource, stage, secret string, cp *stopCheckpoint, p Policy) (output []byte, err error) {
	worker := backupNeedsWorker(s.app.Config.Mode, sources)
	args := []string{"backup", "--json", "--tag", "dm-policy:" + p.ID}
	for _, ex := range p.Excludes {
		if strings.HasPrefix(ex, "-") {
			return nil, errors.New("invalid exclude rule")
		}
		args = append(args, "--exclude", ex)
	}
	if !worker {
		args = append(args, filepath.Join(stage, "manifest.json"))
		for _, src := range sources {
			args = append(args, src.LocalPath)
		}
		return s.restic(t.Context(), r, args...)
	}
	repoHost := r.Location
	stageHost, e := s.hostPath(stage)
	if e != nil {
		return nil, e
	}
	mounts := []mount.Mount{{Type: mount.TypeBind, Source: repoHost, Target: "/repository"}, {Type: mount.TypeBind, Source: stageHost, Target: "/manifest", ReadOnly: true}}
	args = append(args, "/manifest/manifest.json")
	for _, src := range sources {
		typ := mount.TypeBind
		source := src.HostPath
		if src.Volume != "" {
			typ = mount.TypeVolume
			source = src.Volume
		}
		mounts = append(mounts, mount.Mount{Type: typ, Source: source, Target: src.SnapshotPath, ReadOnly: true})
		args = append(args, src.SnapshotPath)
	}
	cfg := &container.Config{Image: s.app.Config.BackupWorkerImage, Entrypoint: []string{"restic"}, Cmd: args, Env: []string{"RESTIC_REPOSITORY=/repository", "RESTIC_PASSWORD=" + secret, "HOME=/tmp", "TMPDIR=/tmp", "RESTIC_CACHE_DIR=/tmp/restic-cache"}, Labels: map[string]string{"docker-manager.worker": "backup", "docker-manager.task": t.ID}}
	host := &container.HostConfig{Mounts: mounts, ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"DAC_READ_SEARCH"}, SecurityOpt: []string{"no-new-privileges:true"}, Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,mode=1777,size=64m"}, NetworkMode: "none"}
	// Execute as repository owner to avoid root-owned data directories in a
	// repository that the non-root manager must subsequently read and maintain.
	repositoryLocal, e := s.path(r.Location)
	if e != nil {
		return nil, e
	}
	st, e := os.Stat(repositoryLocal)
	if e != nil {
		return nil, e
	}
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if stat, ok := st.Sys().(*syscall.Stat_t); ok {
		uid = stat.Uid
		gid = stat.Gid
	}
	cfg.User = "0:0"
	if uid != 0 {
		cfg.Entrypoint = []string{"/usr/local/bin/docklane", "backup-worker"}
		cfg.Env = append(cfg.Env, fmt.Sprintf("DM_WORKER_UID=%d", uid), fmt.Sprintf("DM_WORKER_GID=%d", gid))
		host.CapAdd = []string{"DAC_READ_SEARCH", "SETUID", "SETGID", "SETPCAP"}
	}

	created, e := s.app.Docker.ContainerCreate(t.Context(), cfg, host, nil, nil, "")
	if e != nil {
		return nil, fmt.Errorf("backup worker unavailable; install the pinned worker image: %w", e)
	}
	cp.Worker = created.ID
	if e = t.Checkpoint(cp); e != nil {
		return nil, e
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		removeErr := s.app.Docker.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		if removeErr != nil && !errdefs.IsNotFound(removeErr) {
			err = &CompensationError{Err: errors.Join(err, fmt.Errorf("backup worker removal failed: %w", removeErr))}
			return
		}
		cp.Worker = ""
		if checkpointErr := t.Checkpoint(cp); checkpointErr != nil {
			err = errors.Join(err, checkpointErr)
		}
	}()
	if e = s.app.Docker.ContainerStart(t.Context(), created.ID, container.StartOptions{}); e != nil {
		return nil, e
	}
	wait, errs := s.app.Docker.ContainerWait(t.Context(), created.ID, container.WaitConditionNotRunning)
	select {
	case e = <-errs:
		if e != nil {
			return nil, e
		}
		return nil, errors.New("worker wait ended without exit state")
	case result := <-wait:
		if result.StatusCode != 0 {
			return nil, s.workerFailure(t, created.ID, fmt.Sprintf("backup worker exited with status %d; check source permissions and repository free space", result.StatusCode), secret)
		}
	case <-t.Context().Done():
		return nil, t.Context().Err()
	}
	logs, e := s.app.Docker.ContainerLogs(t.Context(), created.ID, container.LogsOptions{ShowStdout: true})
	if e != nil {
		return nil, e
	}
	defer logs.Close()
	var out limitedBuffer
	_, e = copyDockerOutput(&out, logs)
	if out.Overflow {
		return nil, errors.New("backup worker output exceeded the 16 MiB safety limit")
	}
	return out.Bytes(), e
}

func overlaps(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) || a == "/" || b == "/"
}

func validDockerID(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// recordMissedSchedules reports offline occurrences without executing them.
func (s *Service) recordMissedSchedules() error {
	var stamp string
	err := s.app.DB.QueryRow(`SELECT value FROM workflow_state WHERE key='scheduler_seen'`).Scan(&stamp)
	until := time.Now().UTC()
	if err == nil {
		last, e := time.Parse(time.RFC3339Nano, stamp)
		if e != nil {
			return e
		}
		rows, e := s.app.DB.Query(`SELECT definition FROM backup_policies`)
		if e != nil {
			return e
		}
		policies := []Policy{}
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				rows.Close()
				return e
			}
			var p Policy
			if e = json.Unmarshal([]byte(raw), &p); e != nil {
				rows.Close()
				return e
			}
			policies = append(policies, p)
		}
		rows.Close()
		for _, p := range policies {
			if !p.Enabled || p.Schedule == "" {
				continue
			}
			schedule, e := cron.ParseStandard("CRON_TZ=" + p.Timezone + " " + p.Schedule)
			if e != nil {
				return e
			}
			count := 0
			for at := schedule.Next(last); at.Before(until); at = schedule.Next(at) {
				if count >= 10000 {
					_, e = s.app.DB.Exec(`INSERT OR IGNORE INTO schedule_instances(policy_id,scheduled_at_utc,error) VALUES(?,?,'MISSED_OFFLINE: more than 10000 intervals; remaining history summarized')`, p.ID, until.Format(time.RFC3339))
					if e != nil {
						return e
					}
					break
				}
				_, e = s.app.DB.Exec(`INSERT OR IGNORE INTO schedule_instances(policy_id,scheduled_at_utc,error) VALUES(?,?,'MISSED_OFFLINE: not executed automatically')`, p.ID, at.UTC().Format(time.RFC3339))
				if e != nil {
					return e
				}
				count++
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.app.DB.Exec(`INSERT INTO workflow_state(key,value) VALUES('scheduler_seen',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, until.Format(time.RFC3339Nano))
	return err
}

func (s *Service) backupPreflight(ctx context.Context, p Policy) ([]backupSource, []string, string, error) {
	r, e := s.getRepository(p.RepositoryID)
	if e != nil {
		return nil, nil, "", e
	}
	sources, _, _, e := s.sources(ctx, p)
	if e != nil {
		return nil, nil, "", e
	}
	if e = s.validateSources(r, sources); e != nil {
		return nil, nil, "", e
	}
	writers := []string{}
	if p.Consistency == "stop" {
		writers, e = s.writers(ctx, sources)
		if e != nil {
			return nil, nil, "", e
		}
	}
	sort.Strings(writers)
	return sources, writers, hash([]byte(jsonString(map[string]any{"policy": p, "sources": sources, "writers": writers}))), nil
}
func (s *Service) previewBackup(c *gin.Context) {
	p, e := s.getPolicy(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	sources, writers, revision, e := s.backupPreflight(c.Request.Context(), p)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"revision": revision, "sources": sources, "stop_containers": writers, "warning": "Stop mode includes all known containers writing these mounts, including containers outside the selected project. External host processes cannot be detected; live file mode does not guarantee database consistency."})
}
func (s *Service) scheduleInstances(c *gin.Context) {
	rows, e := s.app.DB.Query(`SELECT policy_id,scheduled_at_utc,task_id,error FROM schedule_instances ORDER BY scheduled_at_utc DESC LIMIT 1000`)
	if e != nil {
		fail(c, e)
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var policy, at string
		var taskID, err sql.NullString
		if e = rows.Scan(&policy, &at, &taskID, &err); e != nil {
			fail(c, e)
			return
		}
		out = append(out, gin.H{"policy_id": policy, "scheduled_at_utc": at, "task_id": taskID.String, "error": err.String})
	}
	c.JSON(200, out)
}

func (s *Service) updatePolicy(c *gin.Context) {
	old, e := s.getPolicy(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	var p Policy
	if !bind(c, &p) {
		return
	}
	p.ID = old.ID
	p.HostID = "local"
	if e = validatePolicy(p); e != nil {
		fail(c, e)
		return
	}
	r, e := s.getRepository(p.RepositoryID)
	if e != nil {
		fail(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
	defer cancel()
	sources, _, _, e := s.sources(ctx, p)
	if e == nil {
		e = s.validateSources(r, sources)
	}
	if e != nil {
		fail(c, e)
		return
	}
	if p.Consistency == "stop" {
		p.AllowedWriters, e = s.writers(ctx, sources)
		if e != nil {
			fail(c, e)
			return
		}
		sort.Strings(p.AllowedWriters)
	}
	_, e = s.app.DB.Exec(`UPDATE backup_policies SET name=?,repository_id=?,definition=? WHERE id=?`, p.Name, p.RepositoryID, jsonString(p), p.ID)
	if e != nil {
		fail(c, e)
		return
	}
	if e = s.loadSchedules(); e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, p)
}

func (s *Service) snapshotMetadata(c *gin.Context) {
	r, e := s.getRepository(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	m, _, e := s.readManifest(ctx, r, c.Param("snapshot"))
	if e != nil {
		fail(c, e)
		return
	}
	cts := []any{}
	for _, raw := range m.Containers {
		var ct container.InspectResponse
		if e = json.Unmarshal(raw, &ct); e != nil {
			fail(c, e)
			return
		}
		project := ""
		if ct.Config != nil {
			project = ct.Config.Labels["com.docker.compose.project"]
		}
		cts = append(cts, gin.H{"id": ct.ID, "name": strings.TrimPrefix(ct.Name, "/"), "image": ct.Image, "compose_project": project})
	}
	projects := []any{}
	for _, p := range m.Compose {
		projects = append(projects, gin.H{"id": p.ID, "name": p.Name})
	}
	c.Header("Cache-Control", "no-store")
	verification, verificationWarning, e := s.snapshotVerification(ctx, r, c.Param("snapshot"))
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"sources": m.Sources, "containers": cts, "projects": projects, "warning": m.ConsistencyWarning, "verification_status": verification, "verification_warning": verificationWarning})
}

func (s *Service) composeDependencies(ctx context.Context, p Project) ([]string, error) {
	files := []string{}
	for _, f := range append(append([]string{}, p.ConfigPaths...), p.EnvFiles...) {
		if !filepath.IsAbs(f) {
			f = filepath.Join(p.Workdir, f)
		}
		files = append(files, f)
	}
	b, e := s.compose(ctx, p, "config", "--no-env-resolution", "--format", "json")
	if e != nil {
		return nil, e
	}
	var compiled map[string]any
	if e = json.Unmarshal(b, &compiled); e != nil {
		return nil, e
	}
	add := func(path string, required bool) error {
		if path == "" {
			return nil
		}
		if !filepath.IsAbs(path) {
			return errors.New("Compose returned a non-absolute dependency path")
		}
		host, e := s.hostPath(path)
		if e != nil {
			return e
		}
		local, e := s.path(host)
		if e != nil {
			return e
		}
		if _, e = os.Stat(local); e != nil {
			if os.IsNotExist(e) && !required {
				return nil
			}
			return e
		}
		files = append(files, host)
		return nil
	}
	if services, ok := compiled["services"].(map[string]any); ok {
		for _, raw := range services {
			service, _ := raw.(map[string]any)
			if envs, ok := service["env_file"].([]any); ok {
				for _, value := range envs {
					switch entry := value.(type) {
					case string:
						if e = add(entry, true); e != nil {
							return nil, e
						}
					case map[string]any:
						path, _ := entry["path"].(string)
						required := true
						if value, ok := entry["required"].(bool); ok {
							required = value
						}
						if e = add(path, required); e != nil {
							return nil, e
						}
					default:
						return nil, errors.New("unsupported Compose env_file shape")
					}
				}
			}
		}
	}
	for _, kind := range []string{"configs", "secrets"} {
		if entries, ok := compiled[kind].(map[string]any); ok {
			for _, raw := range entries {
				entry, _ := raw.(map[string]any)
				if path, ok := entry["file"].(string); ok {
					if e = add(path, true); e != nil {
						return nil, e
					}
				}
			}
		}
	}
	return files, nil
}
