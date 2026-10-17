package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/safepath"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/gin-gonic/gin"
)

type Project struct {
	ID           string            `json:"id"`
	HostID       string            `json:"host_id"`
	Name         string            `json:"name"`
	Source       string            `json:"source_type"`
	Workdir      string            `json:"workdir"`
	ConfigPaths  []string          `json:"config_paths"`
	Environment  map[string]string `json:"environment"`
	EnvFiles     []string          `json:"env_files"`
	Profiles     []string          `json:"profiles"`
	Revision     string            `json:"revision"`
	LastDeployed string            `json:"last_deployed_at"`
	Changed      bool              `json:"changed"`
	Unavailable  string            `json:"unavailable,omitempty"`
}

func (s *Service) registerCompose(r *gin.RouterGroup) {
	r.GET("/compose/discover", s.discover)
	r.GET("/compose/projects", s.projects)
	r.POST("/compose/import", s.audit("importProject", s.importProject))
	r.GET("/compose/projects/:id", s.projectDetail)
	r.POST("/compose/projects/:id/preview", s.audit("previewProject", s.previewProject))
	r.PUT("/compose/projects/:id/files", s.audit("editProject", s.editProject))
	r.POST("/compose/projects/:id/deploy", s.audit("deployProject", s.deployProject))
}
func (s *Service) discover(c *gin.Context) {
	cs, e := s.app.Docker.ContainerList(c.Request.Context(), container.ListOptions{All: true})
	if e != nil {
		fail(c, e)
		return
	}
	projects := map[string]*Project{}
	candidates := map[string]map[string]any{}
	for _, ct := range cs {
		label := ct.Labels
		n := label["com.docker.compose.project"]
		if n == "" {
			continue
		}
		p := projects[n]
		if p == nil {
			p = &Project{HostID: "local", Name: n, Source: "local", Workdir: label["com.docker.compose.project.working_dir"], ConfigPaths: strings.Split(label["com.docker.compose.project.config_files"], ","), Environment: map[string]string{}, Profiles: []string{}, EnvFiles: []string{}}
			projects[n] = p
			candidates[n] = map[string]any{}
		}
		service := label["com.docker.compose.service"]
		if service != "" {
			insp, err := s.app.Docker.ContainerInspect(c.Request.Context(), ct.ID)
			if err == nil && insp.Config != nil {
				candidates[n][service] = map[string]any{"image": insp.Config.Image, "command": insp.Config.Cmd, "environment": insp.Config.Env}
			}
		}
	}
	out := []any{}
	for _, p := range projects {
		_, _, err := s.projectContext(*p)
		if err != nil {
			p.Unavailable = err.Error()
			p.Source = "candidate"
		}
		out = append(out, gin.H{"project": p, "candidate_yaml": jsonString(map[string]any{"services": candidates[p.Name]}), "candidate_status": "pending_validation", "warning": "Candidate cannot reproduce all labels, mounts, ports, secrets or dependencies; review before use"})
	}
	c.JSON(200, out)
}
func (s *Service) projectContext(p Project) (string, []string, error) {
	dir, e := s.path(p.Workdir)
	if e != nil {
		return "", nil, e
	}
	st, e := os.Stat(dir)
	if e != nil || !st.IsDir() {
		return "", nil, errors.New("Compose working directory is unavailable")
	}
	if len(p.ConfigPaths) == 0 {
		return "", nil, errors.New("at least one Compose config is required")
	}
	paths := []string{}
	for _, v := range append(append([]string{}, p.ConfigPaths...), p.EnvFiles...) {
		if !filepath.IsAbs(v) {
			v = filepath.Join(p.Workdir, v)
		}
		q, e := s.path(v)
		if e != nil {
			return "", nil, e
		}
		st, e := os.Stat(q)
		if e != nil || !st.Mode().IsRegular() {
			return "", nil, errors.New("Compose file is unavailable")
		}
		paths = append(paths, q)
	}
	return dir, paths, nil
}
func (s *Service) revision(p Project) (string, error) {
	dir, paths, e := s.projectContext(p)
	if e != nil {
		return "", e
	}
	var b strings.Builder
	b.WriteString(jsonString(p.Environment))
	b.WriteString(jsonString(p.Profiles))
	for _, q := range paths {
		data, e := os.ReadFile(q)
		if e != nil {
			return "", e
		}
		b.WriteString(q)
		b.Write(data)
	}
	dot := filepath.Join(dir, ".env")
	if _, e := os.Stat(dot); e == nil {
		q, e := safepath.Resolve(dot, []string{dir})
		if e != nil {
			return "", e
		}
		data, e := os.ReadFile(q)
		if e != nil {
			return "", e
		}
		b.Write(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resolved, e := s.compose(ctx, p, "config", "--format", "json")
	if e != nil {
		b.WriteString("invalid-compose-context")
	} else {
		b.Write(resolved)
	}
	return hash([]byte(b.String())), nil
}
func composeArgs(p Project, paths []string, op ...string) []string {
	args := []string{"compose", "--project-name", p.Name}
	for _, f := range paths[:len(p.ConfigPaths)] {
		args = append(args, "--file", f)
	}
	for _, f := range paths[len(p.ConfigPaths):] {
		args = append(args, "--env-file", f)
	}
	for _, profile := range p.Profiles {
		args = append(args, "--profile", profile)
	}
	return append(args, op...)
}
func (s *Service) compose(ctx context.Context, p Project, op ...string) ([]byte, error) {
	dir, paths, e := s.projectContext(p)
	if e != nil {
		return nil, e
	}
	if p.Name == "" || strings.HasPrefix(p.Name, "-") {
		return nil, errors.New("invalid Compose project name")
	}
	env := []string{"DOCKER_HOST=" + s.app.Config.DockerEndpoint, "DOCKER_CONTEXT=", "COMPOSE_FILE=", "COMPOSE_PROJECT_NAME=", "COMPOSE_PROFILES=", "COMPOSE_ENV_FILES="}
	for k, v := range p.Environment {
		if !validEnvironment(k) {
			return nil, errors.New("invalid interpolation environment key")
		}
		if dangerousEnvironment(k) {
			return nil, errors.New("tool control environment variables are forbidden")
		}
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return run(ctx, dir, env, s.app.Config.ComposePath, composeArgs(p, paths, op...)...)
}
func validEnvironment(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func (s *Service) getProject(id string) (Project, error) {
	var p Project
	var files, env, envfiles, profiles string
	var last *string
	e := s.app.DB.QueryRow(`SELECT id,host_id,name,source_type,workdir,config_paths,environment,env_files,profiles,revision,last_deployed_at FROM compose_projects WHERE id=?`, id).Scan(&p.ID, &p.HostID, &p.Name, &p.Source, &p.Workdir, &files, &env, &envfiles, &profiles, &p.Revision, &last)
	if e != nil {
		return p, e
	}
	_ = json.Unmarshal([]byte(files), &p.ConfigPaths)
	plaintext, decryptErr := s.app.Vault.Open(env)
	if decryptErr != nil {
		return p, decryptErr
	}
	_ = json.Unmarshal([]byte(plaintext), &p.Environment)
	_ = json.Unmarshal([]byte(envfiles), &p.EnvFiles)
	_ = json.Unmarshal([]byte(profiles), &p.Profiles)
	if last != nil {
		p.LastDeployed = *last
	}
	rev, err := s.revision(p)
	p.Changed = err != nil || rev != p.Revision
	if err != nil {
		p.Unavailable = err.Error()
	}
	return p, nil
}
func (s *Service) projects(c *gin.Context) {
	rows, e := s.app.DB.Query(`SELECT id FROM compose_projects ORDER BY name`)
	if e != nil {
		fail(c, e)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			fail(c, e)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []Project{}
	for _, id := range ids {
		p, e := s.getProject(id)
		if e != nil {
			fail(c, e)
			return
		}
		p.Environment = nil
		out = append(out, p)
	}
	c.JSON(200, out)
}
func (s *Service) projectDetail(c *gin.Context) {
	p, e := s.getProject(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	files := []any{}
	_, paths, e := s.projectContext(p)
	if e == nil {
		for i, q := range paths[:len(p.ConfigPaths)] {
			b, e := os.ReadFile(q)
			if e != nil {
				fail(c, e)
				return
			}
			files = append(files, gin.H{"path": p.ConfigPaths[i], "content": string(b)})
		}
	}
	c.JSON(200, gin.H{"project": p, "files": files})
}
func (s *Service) importProject(c *gin.Context) {
	var req struct {
		Project
		Confirm bool `json:"confirm"`
	}
	if !bind(c, &req) {
		return
	}
	if !req.Confirm || req.HostID != "" && req.HostID != "local" {
		fail(c, errors.New("confirm is required for local project association"))
		return
	}
	p := req.Project
	p.ID = id()
	p.HostID = "local"
	p.Source = "local"
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
	defer cancel()
	out, e := s.compose(ctx, p, "config", "--format", "json")
	if e != nil {
		fail(c, e)
		return
	}
	if !json.Valid(out) {
		fail(c, errors.New("Compose returned invalid configuration"))
		return
	}
	p.Revision, e = s.revision(p)
	if e != nil {
		fail(c, e)
		return
	}
	encryptedEnvironment, sealErr := s.app.Vault.Seal(jsonString(p.Environment))
	if sealErr != nil {
		fail(c, sealErr)
		return
	}
	_, e = s.app.DB.Exec(`INSERT INTO compose_projects(id,host_id,name,source_type,workdir,config_paths,environment,env_files,profiles,revision,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.HostID, p.Name, p.Source, p.Workdir, jsonString(p.ConfigPaths), encryptedEnvironment, jsonString(p.EnvFiles), jsonString(p.Profiles), p.Revision, now())
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(201, p)
}
func (s *Service) previewProject(c *gin.Context) {
	p, e := s.getProject(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
	defer cancel()
	b, e := s.compose(ctx, p, "config", "--format", "json")
	if e != nil {
		fail(c, e)
		return
	}
	revision, e := s.revision(p)
	if e != nil {
		fail(c, e)
		return
	}
	var config any
	if e = json.Unmarshal(b, &config); e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"revision": revision, "configuration": config, "warning": "Deployment may recreate services, alter networks and write mounted data. Configuration rollback cannot reverse data writes."})
}
func (s *Service) editProject(c *gin.Context) {
	p, e := s.getProject(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	var req struct {
		Revision string `json:"revision"`
		Files    []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if !bind(c, &req) {
		return
	}
	rev, e := s.revision(p)
	if e != nil || rev != req.Revision {
		fail(c, errors.New("files changed; refresh the project before saving"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	allowed := map[string]bool{}
	for _, f := range p.ConfigPaths {
		allowed[f] = true
	}
	for _, f := range req.Files {
		if !allowed[f.Path] {
			fail(c, errors.New("only indexed Compose configuration files can be edited"))
			return
		}
		if len(f.Content) > 2<<20 {
			fail(c, errors.New("Compose file exceeds 2 MiB"))
			return
		}
	}
	type edit struct {
		path   string
		before []byte
		after  []byte
		mode   os.FileMode
	}
	edits := []edit{}
	for _, f := range req.Files {
		q := f.Path
		if !filepath.IsAbs(q) {
			q = filepath.Join(p.Workdir, q)
		}
		q, e = s.path(q)
		if e != nil {
			fail(c, e)
			return
		}
		before, e := os.ReadFile(q)
		if e != nil {
			fail(c, e)
			return
		}
		st, e := os.Stat(q)
		if e != nil {
			fail(c, e)
			return
		}
		edits = append(edits, edit{q, before, []byte(f.Content), st.Mode().Perm()})
	}
	current, e := s.revision(p)
	if e != nil || current != req.Revision {
		fail(c, errors.New("files changed while preparing edit; reload"))
		return
	}
	for i, ed := range edits {
		if e = safepath.AtomicWrite(ed.path, ed.after, ed.mode); e != nil {
			errs := []error{e}
			for _, old := range edits[:i] {
				errs = append(errs, safepath.AtomicWrite(old.path, old.before, old.mode))
			}
			fail(c, errors.Join(errs...))
			return
		}
	}

	p.Revision, e = s.revision(p)
	if e == nil {
		_, e = s.app.DB.Exec(`UPDATE compose_projects SET revision=? WHERE id=?`, p.Revision, p.ID)
	}
	if e != nil {
		fail(c, e)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	if _, validationErr := s.compose(ctx, p, "config", "--quiet"); validationErr != nil {
		p.Unavailable = "Saved draft does not validate: " + validationErr.Error()
	}
	c.JSON(200, p)
}
func (s *Service) deployProject(c *gin.Context) {
	p, e := s.getProject(c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	var req struct {
		Revision string `json:"revision"`
		Confirm  bool   `json:"confirm"`
	}
	if !bind(c, &req) {
		return
	}
	if !req.Confirm {
		fail(c, errors.New("deployment requires confirmation of service downtime and mounted data risk"))
		return
	}
	if s.app.Config.Mode == "docker" {
		for _, m := range s.app.Config.PathMappings {
			if m.Host != m.Local {
				fail(c, errors.New("Compose deployment requires identical host/container path mappings to preserve relative bind mounts"))
				return
			}
		}
	}
	rev, e := s.revision(p)
	if e != nil || rev != req.Revision {
		fail(c, errors.New("files changed; preview again"))
		return
	}
	platform.Submit(c, s.app, "compose.deploy", p.ID, false, func(t *task.Run) error {
		current, e := s.revision(p)
		if e != nil || current != req.Revision {
			return errors.New("Compose context changed after confirmation")
		}
		t.Progress("validate", 10)
		if _, e = s.compose(t.Context(), p, "config", "--quiet"); e != nil {
			return e
		}
		t.Progress("deploy", 40)
		if _, e = s.compose(t.Context(), p, "up", "--detach", "--wait", "--wait-timeout", "120"); e != nil {
			return e
		}
		t.Progress("verify", 90)
		if _, e = s.compose(t.Context(), p, "ps", "--format", "json"); e != nil {
			return fmt.Errorf("deployment state verification failed: %w", e)
		}
		_, e = s.app.DB.Exec(`UPDATE compose_projects SET revision=?,last_deployed_at=? WHERE id=?`, current, now(), p.ID)
		return e
	})
}

func dangerousEnvironment(k string) bool {
	for _, prefix := range []string{"DOCKER_", "COMPOSE_", "LD_", "DYLD_", "GIT_", "SSH_"} {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	switch k {
	case "PATH", "HOME", "SHELL", "BASH_ENV", "ENV", "IFS", "CDPATH", "GODEBUG", "GOENV", "XDG_CONFIG_HOME":
		return true
	}
	return false
}
