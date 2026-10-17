// Package resources provides authenticated Docker Engine resource operations.
package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"
	"github.com/gin-gonic/gin"
)

type service struct {
	app  *platform.App
	scan chan struct{}
}

func Register(r *gin.RouterGroup, app *platform.App) {
	s := &service{app: app, scan: make(chan struct{}, 1)}
	if _, err := app.DB.Exec(`CREATE TABLE IF NOT EXISTS registry_credentials (id TEXT PRIMARY KEY,name TEXT NOT NULL,server TEXT NOT NULL,username TEXT NOT NULL,secret TEXT NOT NULL,created_at TEXT NOT NULL)`); err != nil {
		panic(fmt.Errorf("registry credentials schema: %w", err))
	}
	app.Tasks.Recover("container.rebuild", s.recoverRebuild)
	h := r.Group("/hosts/:host_id")
	h.Use(func(c *gin.Context) {
		if expires, ok := c.Get("session_expires"); ok {
			if deadline, ok := expires.(time.Time); ok {
				ctx, cancel := context.WithDeadline(c.Request.Context(), deadline)
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
			}
		}
		p := c.Request.URL.Path
		if !strings.HasSuffix(p, "/logs") && !strings.HasSuffix(p, "/terminal") && !strings.HasSuffix(p, "/export") && !strings.HasSuffix(p, "/import") && !(strings.HasSuffix(p, "/stats") && c.Query("stream") == "true") {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	})
	h.Use(func(c *gin.Context) {
		if c.Param("host_id") != "local" {
			platform.Fail(c, 404, "HOST_NOT_FOUND", nil)
			return
		}
		c.Next()
	})
	h.GET("/containers", s.containers)
	h.GET("/containers/:id", s.inspectContainer)
	for _, action := range []string{"start", "stop", "restart"} {
		action := action
		h.POST("/containers/:id/"+action, func(c *gin.Context) { s.lifecycle(c, action) })
	}
	h.DELETE("/containers/:id", s.deleteContainer)
	h.POST("/containers/:id/config/preview", s.previewConfig)
	h.POST("/containers/:id/config", s.applyConfig)
	h.GET("/containers/:id/logs", s.logs)
	h.GET("/containers/:id/stats", s.stats)
	h.GET("/containers/:id/terminal", s.terminal)
	h.GET("/images", s.images)
	h.GET("/images/:id", s.inspectImage)
	h.POST("/images/pull", s.pullImage)
	h.POST("/images/import", s.importImage)
	h.GET("/images/:id/export", s.exportImage)
	h.DELETE("/images/:id", s.deleteImage)
	h.GET("/networks", s.networks)
	h.GET("/networks/:id", s.inspectNetwork)
	h.POST("/networks", s.createNetwork)
	h.DELETE("/networks/:id", s.deleteNetwork)
	h.GET("/volumes", s.volumes)
	h.GET("/volumes/:id", s.inspectVolume)
	h.POST("/volumes", s.createVolume)
	h.DELETE("/volumes/:id", s.deleteVolume)
	h.GET("/mounts", s.mounts)
	h.POST("/storage/scan", s.scanDirectory)
	r.GET("/registry-credentials", s.credentials)
	r.POST("/registry-credentials", s.saveCredential)
	r.DELETE("/registry-credentials/:id", s.deleteCredential)
	r.POST("/registry-credentials/:id/test", s.testCredential)
}
func fail(c *gin.Context, e error) {
	status := http.StatusBadGateway
	if errdefs.IsNotFound(e) {
		status = 404
	}
	if errdefs.IsConflict(e) {
		status = 409
	}
	if errdefs.IsInvalidParameter(e) {
		status = 400
	}
	platform.Fail(c, status, "DOCKER_ERROR", e)
}
func bind(c *gin.Context, v any) bool {
	d := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		platform.Fail(c, 400, "INVALID_REQUEST", err)
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		platform.Fail(c, 400, "INVALID_REQUEST", errors.New("one JSON object is required"))
		return false
	}
	return true
}
func confirmed(c *gin.Context) bool {
	var b struct {
		Confirmed bool `json:"confirmed"`
	}
	if !bind(c, &b) {
		return false
	}
	if !b.Confirmed {
		platform.Fail(c, 400, "CONFIRMATION_REQUIRED", nil)
		return false
	}
	return true
}
func page[T any](c *gin.Context, items []T) {
	p, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	n, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if p < 1 {
		p = 1
	}
	if n < 1 {
		n = 50
	}
	if n > 200 {
		n = 200
	}
	start := (p - 1) * n
	if start < 0 || start > len(items) {
		start = len(items)
	}
	end := start + n
	if end > len(items) {
		end = len(items)
	}
	out := items[start:end]
	if out == nil {
		out = []T{}
	}
	c.JSON(200, gin.H{"items": out, "total": len(items), "page": p, "page_size": n})
}
func (s *service) containers(c *gin.Context) {
	v, e := s.app.Docker.ContainerList(c.Request.Context(), container.ListOptions{All: true})
	if e != nil {
		fail(c, e)
		return
	}
	q := strings.ToLower(c.Query("q"))
	state := c.Query("state")
	out := make([]container.Summary, 0, len(v))
	for _, it := range v {
		if state != "" && string(it.State) != state {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.ID+it.Image+strings.Join(it.Names, " ")), q) {
			continue
		}
		out = append(out, it)
	}
	page(c, out)
}
func (s *service) inspectContainer(c *gin.Context) {
	v, e := s.app.Docker.ContainerInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *service) lifecycle(c *gin.Context, action string) {
	id := c.Param("id")
	ctx := c.Request.Context()
	v, e := s.app.Docker.ContainerInspect(ctx, id)
	if e != nil {
		fail(c, e)
		return
	}
	id = v.ID
	platform.Submit(c, s.app, "container."+action, "container:"+id, false, func(r *task.Run) error {
		r.Progress(action, 10)
		ctx := r.Context()
		var e error
		switch action {
		case "start":
			e = s.app.Docker.ContainerStart(ctx, id, container.StartOptions{})
		case "stop":
			e = s.app.Docker.ContainerStop(ctx, id, container.StopOptions{})
		case "restart":
			e = s.app.Docker.ContainerRestart(ctx, id, container.StopOptions{})
		}
		if e != nil {
			return e
		}
		actual, e := s.app.Docker.ContainerInspect(ctx, id)
		if e != nil {
			return e
		}
		if actual.State == nil {
			return errors.New("Engine returned no container state")
		}
		if action == "stop" && actual.State.Running {
			return errors.New("container remains running")
		}
		if action != "stop" && !actual.State.Running {
			return fmt.Errorf("container did not remain running: %s", actual.State.Status)
		}
		r.Log("Engine state: " + string(actual.State.Status))
		r.Progress("verified", 100)
		return nil
	})
}
func (s *service) deleteContainer(c *gin.Context) {
	if !confirmed(c) {
		return
	}
	v, e := s.app.Docker.ContainerInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	if v.State != nil && v.State.Running {
		platform.Fail(c, 409, "CONTAINER_RUNNING", errors.New("stop container before deleting; data volumes are retained"))
		return
	}
	platform.Submit(c, s.app, "container.delete", "container:"+v.ID, false, func(r *task.Run) error {
		if e := s.app.Docker.ContainerRemove(r.Context(), v.ID, container.RemoveOptions{RemoveVolumes: false, Force: false}); e != nil {
			return e
		}
		_, e := s.app.Docker.ContainerInspect(r.Context(), v.ID)
		if !errdefs.IsNotFound(e) {
			return errors.New("Engine did not confirm container removal")
		}
		r.Log("Container removed; volumes and bind mount files retained")
		return nil
	})
}
func (s *service) images(c *gin.Context) {
	v, e := s.app.Docker.ImageList(c.Request.Context(), image.ListOptions{All: true})
	if e != nil {
		fail(c, e)
		return
	}
	q := strings.ToLower(c.Query("q"))
	out := make([]image.Summary, 0, len(v))
	for _, x := range v {
		if q == "" || strings.Contains(strings.ToLower(x.ID+strings.Join(x.RepoTags, " ")), q) {
			out = append(out, x)
		}
	}
	page(c, out)
}
func (s *service) inspectImage(c *gin.Context) {
	v, e := s.app.Docker.ImageInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	refs, e := s.imageReferences(c.Request.Context(), v.ID)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"image": v, "containers": refs})
}
func (s *service) imageReferences(ctx context.Context, id string) ([]container.Summary, error) {
	all, e := s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		return nil, e
	}
	out := []container.Summary{}
	for _, x := range all {
		if x.ImageID == id {
			out = append(out, x)
		}
	}
	return out, nil
}
func (s *service) deleteImage(c *gin.Context) {
	if !confirmed(c) {
		return
	}
	v, e := s.app.Docker.ImageInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	refs, e := s.imageReferences(c.Request.Context(), v.ID)
	if e != nil {
		fail(c, e)
		return
	}
	if len(refs) > 0 {
		c.JSON(409, gin.H{"error": gin.H{"code": "IMAGE_REFERENCED", "message": "image is referenced by containers"}, "containers": refs})
		return
	}
	platform.Submit(c, s.app, "image.delete", "image:"+v.ID, false, func(r *task.Run) error {
		refs, e := s.imageReferences(r.Context(), v.ID)
		if e != nil {
			return e
		}
		if len(refs) > 0 {
			return errors.New("image acquired container references")
		}
		_, e = s.app.Docker.ImageRemove(r.Context(), v.ID, image.RemoveOptions{Force: false, PruneChildren: false})
		return e
	})
}
func (s *service) networks(c *gin.Context) {
	v, e := s.app.Docker.NetworkList(c.Request.Context(), network.ListOptions{})
	if e != nil {
		fail(c, e)
		return
	}
	q := strings.ToLower(c.Query("q"))
	out := make([]network.Summary, 0, len(v))
	for _, x := range v {
		if q == "" || strings.Contains(strings.ToLower(x.Name+" "+x.ID+" "+x.Driver), q) {
			out = append(out, x)
		}
	}
	page(c, out)
}
func (s *service) inspectNetwork(c *gin.Context) {
	v, e := s.app.Docker.NetworkInspect(c.Request.Context(), c.Param("id"), network.InspectOptions{})
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, v)
}
func (s *service) createNetwork(c *gin.Context) {
	var b struct {
		Name     string            `json:"name"`
		Driver   string            `json:"driver"`
		Internal bool              `json:"internal"`
		Labels   map[string]string `json:"labels"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Name == "" {
		platform.Fail(c, 400, "NAME_REQUIRED", nil)
		return
	}
	if b.Driver == "" {
		b.Driver = "bridge"
	}
	v, e := s.app.Docker.NetworkCreate(c.Request.Context(), b.Name, network.CreateOptions{Driver: b.Driver, Internal: b.Internal, Labels: b.Labels})
	if e != nil {
		fail(c, e)
		return
	}
	if !s.auditMutation(c, "network.create", "network", v.ID) {
		return
	}
	c.JSON(201, v)
}
func (s *service) deleteNetwork(c *gin.Context) {
	if !confirmed(c) {
		return
	}
	v, e := s.app.Docker.NetworkInspect(c.Request.Context(), c.Param("id"), network.InspectOptions{})
	if e != nil {
		fail(c, e)
		return
	}
	if v.Name == "bridge" || v.Name == "host" || v.Name == "none" || len(v.Containers) > 0 {
		platform.Fail(c, 409, "NETWORK_REFERENCED", errors.New("built-in or referenced network cannot be deleted"))
		return
	}
	if e = s.app.Docker.NetworkRemove(c.Request.Context(), v.ID); e != nil {
		fail(c, e)
		return
	}
	if !s.auditMutation(c, "network.delete", "network", v.ID) {
		return
	}
	c.JSON(200, gin.H{"deleted": v.ID})
}
func (s *service) volumes(c *gin.Context) {
	v, e := s.app.Docker.VolumeList(c.Request.Context(), volume.ListOptions{})
	if e != nil {
		fail(c, e)
		return
	}
	q := strings.ToLower(c.Query("q"))
	out := make([]*volume.Volume, 0, len(v.Volumes))
	for _, x := range v.Volumes {
		if x != nil && (q == "" || strings.Contains(strings.ToLower(x.Name+" "+x.Driver), q)) {
			out = append(out, x)
		}
	}
	page(c, out)
}
func (s *service) volumeReferences(ctx context.Context, name string) ([]container.Summary, error) {
	all, e := s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		return nil, e
	}
	refs := []container.Summary{}
	for _, x := range all {
		for _, m := range x.Mounts {
			if m.Type == "volume" && m.Name == name {
				refs = append(refs, x)
				break
			}
		}
	}
	return refs, nil
}
func (s *service) inspectVolume(c *gin.Context) {
	v, e := s.app.Docker.VolumeInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	refs, e := s.volumeReferences(c.Request.Context(), v.Name)
	if e != nil {
		fail(c, e)
		return
	}
	c.JSON(200, gin.H{"volume": v, "containers": refs})
}
func (s *service) createVolume(c *gin.Context) {
	var b struct {
		Name   string            `json:"name"`
		Driver string            `json:"driver"`
		Labels map[string]string `json:"labels"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Name == "" {
		platform.Fail(c, 400, "NAME_REQUIRED", nil)
		return
	}
	if b.Driver == "" {
		b.Driver = "local"
	}
	v, e := s.app.Docker.VolumeCreate(c.Request.Context(), volume.CreateOptions{Name: b.Name, Driver: b.Driver, Labels: b.Labels})
	if e != nil {
		fail(c, e)
		return
	}
	if !s.auditMutation(c, "volume.create", "volume", v.Name) {
		return
	}
	c.JSON(201, v)
}
func (s *service) deleteVolume(c *gin.Context) {
	if !confirmed(c) {
		return
	}
	name := c.Param("id")
	refs, e := s.volumeReferences(c.Request.Context(), name)
	if e != nil {
		fail(c, e)
		return
	}
	if len(refs) > 0 {
		c.JSON(409, gin.H{"error": gin.H{"code": "VOLUME_REFERENCED", "message": "stopped containers also retain references"}, "containers": refs})
		return
	}
	if e = s.app.Docker.VolumeRemove(c.Request.Context(), name, false); e != nil {
		fail(c, e)
		return
	}
	if !s.auditMutation(c, "volume.delete", "volume", name) {
		return
	}
	c.JSON(200, gin.H{"deleted": name})
}
func (s *service) mounts(c *gin.Context) {
	all, e := s.app.Docker.ContainerList(c.Request.Context(), container.ListOptions{All: true})
	if e != nil {
		fail(c, e)
		return
	}
	type entry struct {
		ContainerID string               `json:"container_id"`
		Names       []string             `json:"names"`
		Mount       container.MountPoint `json:"mount"`
	}
	out := []entry{}
	for _, x := range all {
		for _, m := range x.Mounts {
			out = append(out, entry{x.ID, x.Names, m})
		}
	}
	page(c, out)
}
func (s *service) stats(c *gin.Context) {
	stream := c.Query("stream") == "true"
	ctx := c.Request.Context()
	v, e := s.app.Docker.ContainerStats(ctx, c.Param("id"), stream)
	if e != nil {
		fail(c, e)
		return
	}
	defer v.Body.Close()
	decoder := json.NewDecoder(v.Body)
	if stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Accel-Buffering", "no")
		c.Writer.Flush()
	}
	for {
		var raw json.RawMessage
		e = decoder.Decode(&raw)
		if e != nil {
			if !stream {
				fail(c, e)
			}
			return
		}
		if !stream {
			c.Data(200, "application/json", raw)
			return
		}
		if _, e = fmt.Fprintf(c.Writer, "event: stats\ndata: %s\n\n", raw); e != nil {
			return
		}
		c.Writer.Flush()
	}
}

func (s *service) auditMutation(c *gin.Context, action, kind, id string) bool {
	if e := database.Audit(s.app.DB, platform.Actor(c), action, kind, id, "succeeded"); e != nil {
		platform.Fail(c, 500, "AUDIT_WRITE_FAILED", errors.New("resource operation completed, but its audit entry could not be saved"))
		return false
	}
	return true
}
