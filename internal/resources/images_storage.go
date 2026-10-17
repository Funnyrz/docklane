package resources

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/safepath"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/gin-gonic/gin"
)

type credential struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Server    string `json:"server"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

func (s *service) credentials(c *gin.Context) {
	rows, e := s.app.DB.QueryContext(c.Request.Context(), `SELECT id,name,server,username,created_at FROM registry_credentials ORDER BY created_at`)
	if e != nil {
		platform.Fail(c, 500, "DATABASE_ERROR", e)
		return
	}
	defer rows.Close()
	out := []credential{}
	for rows.Next() {
		var v credential
		if e = rows.Scan(&v.ID, &v.Name, &v.Server, &v.Username, &v.CreatedAt); e != nil {
			platform.Fail(c, 500, "DATABASE_ERROR", e)
			return
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		platform.Fail(c, 500, "DATABASE_ERROR", e)
		return
	}
	c.JSON(200, out)
}
func (s *service) saveCredential(c *gin.Context) {
	var b struct {
		Name     string `json:"name"`
		Server   string `json:"server"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Name == "" || b.Server == "" || b.Username == "" || b.Password == "" || len(b.Password) > 16384 {
		platform.Fail(c, 400, "CREDENTIAL_FIELDS_REQUIRED", nil)
		return
	}
	if strings.ContainsAny(b.Server, "\r\n ") {
		platform.Fail(c, 400, "INVALID_REGISTRY", nil)
		return
	}
	serverURL := b.Server
	if !strings.Contains(serverURL, "://") {
		serverURL = "https://" + serverURL
	}
	parsed, parseErr := url.Parse(serverURL)
	if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		// Credentials belong in the encrypted field, never in the plain registry URL.
		platform.Fail(c, 400, "INVALID_REGISTRY", nil)
		return
	}
	secret, e := s.app.Vault.Seal(b.Password)
	if e != nil {
		platform.Fail(c, 500, "ENCRYPTION_ERROR", nil)
		return
	}
	v := credential{database.ID(), b.Name, b.Server, b.Username, database.Now()}
	_, e = s.app.DB.ExecContext(c.Request.Context(), `INSERT INTO registry_credentials(id,name,server,username,secret,created_at) VALUES(?,?,?,?,?,?)`, v.ID, v.Name, v.Server, v.Username, secret, v.CreatedAt)
	if e != nil {
		platform.Fail(c, 500, "DATABASE_ERROR", e)
		return
	}
	if !s.auditMutation(c, "registry.create", "registry", v.ID) {
		return
	}
	c.JSON(201, v)
}
func (s *service) deleteCredential(c *gin.Context) {
	if !confirmed(c) {
		return
	}
	res, e := s.app.DB.ExecContext(c.Request.Context(), `DELETE FROM registry_credentials WHERE id=?`, c.Param("id"))
	if e != nil {
		platform.Fail(c, 500, "DATABASE_ERROR", e)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		platform.Fail(c, 404, "CREDENTIAL_NOT_FOUND", nil)
		return
	}
	if !s.auditMutation(c, "registry.delete", "registry", c.Param("id")) {
		return
	}
	c.JSON(200, gin.H{"deleted": c.Param("id")})
}
func registryHost(imageName string) string {
	first, _, qualified := strings.Cut(imageName, "/")
	if qualified && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		if first == "docker.io" || first == "registry-1.docker.io" {
			return "index.docker.io"
		}
		return first
	}
	return "index.docker.io"
}
func credentialHost(server string) string {
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	u, e := url.Parse(server)
	if e != nil {
		return ""
	}
	h := u.Hostname()
	if u.Port() != "" {
		h = u.Host
	}
	if h == "docker.io" || h == "registry-1.docker.io" {
		h = "index.docker.io"
	}
	return h
}
func (s *service) pullImage(c *gin.Context) {
	var b struct {
		Image        string `json:"image"`
		CredentialID string `json:"credential_id"`
		Platform     string `json:"platform"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Image == "" || strings.HasPrefix(b.Image, "-") || strings.ContainsAny(b.Image, " \t\r\n") {
		platform.Fail(c, 400, "INVALID_IMAGE", nil)
		return
	}
	auth := ""
	var sensitiveValues []string
	if b.CredentialID != "" {
		var server, user, secret string
		e := s.app.DB.QueryRowContext(c.Request.Context(), `SELECT server,username,secret FROM registry_credentials WHERE id=?`, b.CredentialID).Scan(&server, &user, &secret)
		if e != nil {
			platform.Fail(c, 404, "CREDENTIAL_NOT_FOUND", nil)
			return
		}
		if credentialHost(server) != registryHost(b.Image) {
			platform.Fail(c, 400, "CREDENTIAL_REGISTRY_MISMATCH", errors.New("credential registry does not match image registry"))
			return
		}
		password, e := s.app.Vault.Open(secret)
		if e != nil {
			platform.Fail(c, 500, "DECRYPTION_ERROR", nil)
			return
		}
		encoded, _ := json.Marshal(registry.AuthConfig{Username: user, Password: password, ServerAddress: server})
		auth = base64.URLEncoding.EncodeToString(encoded)
		sensitiveValues = []string{password, auth, base64.StdEncoding.EncodeToString([]byte(user + ":" + password))}
	}
	platform.Submit(c, s.app, "image.pull", "image:"+b.Image, true, func(r *task.Run) error {
		r.Progress("pulling", 0)
		reader, e := s.app.Docker.ImagePull(r.Context(), b.Image, image.PullOptions{RegistryAuth: auth, Platform: b.Platform})
		if e != nil {
			if canceled := r.Context().Err(); canceled != nil {
				return canceled
			}
			return errors.New(redactRegistry(e.Error(), sensitiveValues...))
		}
		defer reader.Close()
		if e = readImageProgress(reader, r, sensitiveValues...); e != nil {
			return e
		}
		v, e := s.app.Docker.ImageInspect(r.Context(), b.Image)
		if e != nil {
			return e
		}
		r.Log("Image ID: " + v.ID)
		for _, d := range v.RepoDigests {
			r.Log("Digest: " + d)
		}
		r.Progress("verified", 100)
		return nil
	})
}

// Registry errors are untrusted text: they can echo a credential without a
// recognizable "password=" field. Remove the exact known values before any
// task log, task error or API error can persist or return them.
func redactRegistry(value string, sensitiveValues ...string) string {
	for _, secret := range sensitiveValues {
		if secret == "" {
			continue
		}
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
		encoded, _ := json.Marshal(secret)
		value = strings.ReplaceAll(value, string(encoded[1:len(encoded)-1]), "[REDACTED]")
	}
	return security.Redact(value)
}

func readImageProgress(reader io.Reader, r *task.Run, sensitiveValues ...string) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var v struct {
			Status      string `json:"status"`
			Stream      string `json:"stream"`
			ID          string `json:"id"`
			Progress    string `json:"progress"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if e := json.Unmarshal(scanner.Bytes(), &v); e != nil {
			return fmt.Errorf("invalid Engine progress: %w", e)
		}
		if v.Error != "" {
			return errors.New(redactRegistry(v.Error, sensitiveValues...))
		}
		if v.ErrorDetail.Message != "" {
			return errors.New(redactRegistry(v.ErrorDetail.Message, sensitiveValues...))
		}
		if v.Stream != "" {
			r.Log(redactRegistry(strings.TrimSpace(v.Stream), sensitiveValues...))
		}
		if v.Status != "" {
			r.Log(redactRegistry(strings.TrimSpace(v.ID+" "+v.Status+" "+v.Progress), sensitiveValues...))
		}
	}
	return scanner.Err()
}
func (s *service) importImage(c *gin.Context) {
	dir := filepath.Join(s.app.Config.DataDir, "uploads")
	if e := os.MkdirAll(dir, 0700); e != nil {
		platform.Fail(c, 500, "UPLOAD_ERROR", e)
		return
	}
	f, e := os.CreateTemp(dir, "image-*.tar")
	if e != nil {
		platform.Fail(c, 500, "UPLOAD_ERROR", e)
		return
	}
	path := f.Name()
	c.Request.Body = httpMaxBody(c, 8<<30)
	_, e = io.Copy(f, c.Request.Body)
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		_ = os.Remove(path)
		platform.Fail(c, 400, "UPLOAD_ERROR", e)
		return
	}
	t, e := s.app.Tasks.Submit(c.Request.Context(), "image.import", "image:import", platform.Actor(c), c.GetHeader("Idempotency-Key"), true, func(r *task.Run) error {
		defer os.Remove(path)
		file, e := os.Open(path)
		if e != nil {
			return e
		}
		defer file.Close()
		v, e := s.app.Docker.ImageLoad(r.Context(), file)
		if e != nil {
			return e
		}
		defer v.Body.Close()
		return readImageProgress(v.Body, r)
	})
	if e != nil {
		_ = os.Remove(path)
		platform.Fail(c, 409, "TASK_CONFLICT", e)
		return
	}
	// A pending cancellation or an idempotent response may never invoke this upload's closure.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		deadline := time.NewTimer(31 * time.Minute)
		defer deadline.Stop()
		for {
			current, e := s.app.Tasks.Get(t.ID)
			if e != nil {
				return
			}
			if current.Status != "Pending" && current.Status != "Running" {
				_ = os.Remove(path)
				return
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				return
			}
		}
	}()
	c.JSON(202, t)
}
func (s *service) exportImage(c *gin.Context) {
	v, e := s.app.Docker.ImageInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	reader, e := s.app.Docker.ImageSave(c.Request.Context(), []string{v.ID})
	if e != nil {
		fail(c, e)
		return
	}
	defer reader.Close()
	c.Header("Content-Disposition", `attachment; filename="docker-image.tar"`)
	c.Header("Content-Type", "application/x-tar")
	c.Status(200)
	_, _ = io.Copy(c.Writer, reader)
}
func httpMaxBody(c *gin.Context, n int64) io.ReadCloser {
	return &limitedBody{ReadCloser: c.Request.Body, remaining: n}
}

type limitedBody struct {
	io.ReadCloser
	remaining int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		var b [1]byte
		n, e := l.ReadCloser.Read(b[:])
		if n > 0 {
			return 0, errors.New("upload exceeds maximum size")
		}
		return 0, e
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, e := l.ReadCloser.Read(p)
	l.remaining -= int64(n)
	return n, e
}
func (s *service) resolveScanPath(path string) (string, error) {
	if s.app.Config.Mode != "docker" {
		return safepath.Resolve(path, s.app.Config.AllowedPaths)
	}
	path = filepath.Clean(path)
	for _, m := range s.app.Config.PathMappings {
		rel, e := filepath.Rel(filepath.Clean(m.Host), path)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		authorized := false
		for _, root := range s.app.Config.AllowedPaths {
			r, e := filepath.Rel(filepath.Clean(root), path)
			if e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				authorized = true
				break
			}
		}
		if !authorized {
			return "", errors.New("host directory is not authorized")
		}
		return safepath.Resolve(filepath.Join(m.Local, rel), []string{m.Local})
	}
	return "", errors.New("Docker mode requires an explicit host-to-local path mapping")
}

// openScanRoot pins an authorized directory descriptor before traversal. A queued
// path may have changed since submission, and later ancestor swaps must not
// redirect the walk outside that descriptor's filesystem boundary.
func (s *service) openScanRoot(requested, expected string) (*os.Root, error) {
	current, err := s.resolveScanPath(requested)
	if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, errors.New("scan path changed after authorization; submit a new scan")
	}
	roots := s.app.Config.AllowedPaths
	if s.app.Config.Mode == "docker" {
		roots = nil
		for _, mapping := range s.app.Config.PathMappings {
			roots = append(roots, mapping.Local)
		}
	}
	for _, candidate := range roots {
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil || canonical == string(filepath.Separator) {
			continue
		}
		rel, err := filepath.Rel(canonical, current)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		base, err := os.OpenRoot(canonical)
		if err != nil {
			return nil, err
		}
		root, err := base.OpenRoot(rel)
		_ = base.Close()
		return root, err
	}
	return nil, errors.New("scan directory is outside an authorized filesystem boundary")
}

func (s *service) scanDirectory(c *gin.Context) {
	var b struct {
		Path string `json:"path"`
	}
	if !bind(c, &b) {
		return
	}
	path, e := s.resolveScanPath(b.Path)
	if e != nil {
		platform.Fail(c, 403, "PATH_NOT_AUTHORIZED", e)
		return
	}
	info, e := os.Stat(path)
	if e != nil || !info.IsDir() {
		platform.Fail(c, 400, "NOT_A_DIRECTORY", e)
		return
	}
	platform.Submit(c, s.app, "storage.scan", "directory:"+path, true, func(r *task.Run) error {
		select {
		case s.scan <- struct{}{}:
			defer func() { <-s.scan }()
		case <-r.Context().Done():
			return r.Context().Err()
		}
		root, e := s.openScanRoot(b.Path, path)
		if e != nil {
			return e
		}
		defer root.Close()
		r.Progress("scanning", 0)
		var size, files, dirs int64
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		e = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if e = r.Context().Err(); e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				dirs++
			} else {
				info, e := d.Info()
				if e != nil {
					return e
				}
				if info.Mode().IsRegular() {
					size += info.Size()
					files++
				}
			}
			if (files+dirs)%128 == 0 {
				select {
				case <-tick.C:
				case <-r.Context().Done():
					return r.Context().Err()
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		result := gin.H{"path": b.Path, "bytes": size, "files": files, "directories": dirs}
		if e = r.Checkpoint(result); e != nil {
			return e
		}
		r.Log(fmt.Sprintf("%d bytes in %d files and %d directories (symlinks not followed)", size, files, dirs))
		r.Progress("complete", 100)
		return nil
	})
}

func (s *service) testCredential(c *gin.Context) {
	var server, user, secret string
	e := s.app.DB.QueryRowContext(c.Request.Context(), `SELECT server,username,secret FROM registry_credentials WHERE id=?`, c.Param("id")).Scan(&server, &user, &secret)
	if e != nil {
		platform.Fail(c, 404, "CREDENTIAL_NOT_FOUND", nil)
		return
	}
	password, e := s.app.Vault.Open(secret)
	if e != nil {
		platform.Fail(c, 500, "DECRYPTION_ERROR", nil)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	result, e := s.app.Docker.RegistryLogin(ctx, registry.AuthConfig{ServerAddress: server, Username: user, Password: password})
	if e != nil {
		platform.Fail(c, 502, "REGISTRY_LOGIN_FAILED", errors.New(redactRegistry(e.Error(), password, base64.StdEncoding.EncodeToString([]byte(user+":"+password)))))
		return
	}
	if !s.auditMutation(c, "registry.test", "registry", c.Param("id")) {
		return
	}
	c.JSON(200, gin.H{"status": result.Status, "authenticated": true})
}
