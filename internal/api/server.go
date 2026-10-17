package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/gin-gonic/gin"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

type Register func(*gin.RouterGroup, *platform.App)

func New(a *platform.App, assets fs.FS, features ...Register) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Gin's default recovery dumps request headers, including the session Cookie.
	// Keep panic diagnostics structural so credentials can never enter logs.
	r.Use(func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				slog.Error("request panic", "request_id", c.GetString("request_id"), "route", c.FullPath())
				if !c.Writer.Written() {
					platform.Fail(c, 500, "INTERNAL_ERROR", nil)
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	})
	_ = r.SetTrustedProxies(nil)
	r.Use(func(c *gin.Context) {
		requestID := database.ID()
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none'")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
		}
		var maxBody int64 = 4 << 20
		if c.Request.Method == "POST" && c.Request.URL.Path == "/api/v1/hosts/local/images/import" {
			maxBody = 8 << 30
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
		c.Next()
		if c.Request.Method != "GET" && c.Writer.Status() >= 400 {
			actor := platform.Actor(c)
			if actor == "" {
				actor = "anonymous"
			}
			_ = database.Audit(a.DB, actor, c.Request.Method+" "+c.FullPath(), "http", c.Param("id"), fmt.Sprintf("HTTP_%d", c.Writer.Status()))
		}
	})
	au := &auth{app: a, attempts: map[string][]time.Time{}}
	v1 := r.Group("/api/v1")
	v1.GET("/auth/status", au.status)
	v1.POST("/auth/setup", au.setup)
	v1.POST("/auth/login", au.login)
	p := v1.Group("")
	p.Use(au.require)
	p.POST("/auth/logout", au.logout)
	p.GET("/hosts/:host_id/dashboard", func(c *gin.Context) { dashboard(c, a) })
	p.GET("/hosts/:host_id/capabilities", func(c *gin.Context) {
		c.JSON(200, gin.H{"host_id": "local", "mode": a.Config.Mode, "capabilities": capabilities(c.Request.Context(), a)})
	})
	p.GET("/settings", func(c *gin.Context) {
		c.JSON(200, gin.H{"version": "0.3.0", "mode": a.Config.Mode, "listen": a.Config.Listen, "data_dir": a.Config.DataDir, "allowed_paths": a.Config.AllowedPaths, "path_mappings": a.Config.PathMappings, "capabilities": capabilities(c.Request.Context(), a)})
	})
	p.GET("/tasks", func(c *gin.Context) {
		limit, offset := pagination(c)
		v, e := a.Tasks.List(limit, offset)
		if e != nil {
			platform.Fail(c, 500, "DATABASE_ERROR", nil)
			return
		}
		c.JSON(200, v)
	})
	p.GET("/tasks/:id", func(c *gin.Context) {
		v, e := a.Tasks.Get(c.Param("id"))
		if e != nil {
			platform.Fail(c, 404, "TASK_NOT_FOUND", nil)
			return
		}
		c.JSON(200, v)
	})
	p.GET("/tasks/:id/logs", func(c *gin.Context) {
		after, _ := strconv.ParseInt(c.Query("after"), 10, 64)
		v, e := a.Tasks.Logs(c.Param("id"), after)
		if e != nil {
			platform.Fail(c, 500, "DATABASE_ERROR", nil)
			return
		}
		c.JSON(200, v)
	})
	p.POST("/tasks/:id/cancel", func(c *gin.Context) {
		if e := a.Tasks.Cancel(c.Param("id")); e != nil {
			platform.Fail(c, 409, "CANCEL_DENIED", e)
			return
		}
		c.JSON(200, gin.H{"status": "requested"})
	})
	p.GET("/audit", func(c *gin.Context) {
		limit, offset := pagination(c)
		rows, e := a.DB.Query("SELECT id,actor_id,action,resource_type,resource_id,result,created_at FROM audit_logs ORDER BY created_at DESC LIMIT ? OFFSET ?", limit, offset)
		if e != nil {
			platform.Fail(c, 500, "DATABASE_ERROR", nil)
			return
		}
		defer rows.Close()
		out := []gin.H{}
		for rows.Next() {
			var id, actor, action, kind, target, result, created string
			if e = rows.Scan(&id, &actor, &action, &kind, &target, &result, &created); e != nil {
				platform.Fail(c, 500, "DATABASE_ERROR", nil)
				return
			}
			out = append(out, gin.H{"id": id, "actor_id": actor, "action": action, "resource_type": kind, "resource_id": target, "result": result, "created_at": created})
		}
		c.JSON(200, out)
	})
	p.GET("/events", func(c *gin.Context) { events(c, a) })
	for _, register := range features {
		register(p, a)
	}
	r.GET("/healthz", func(c *gin.Context) {
		if e := a.DB.PingContext(c.Request.Context()); e != nil {
			c.JSON(503, gin.H{"status": "database_error"})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || c.Request.Method != "GET" {
			platform.Fail(c, 404, "NOT_FOUND", nil)
			return
		}
		name := strings.TrimPrefix(path.Clean(c.Request.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		if _, e := fs.Stat(assets, name); e != nil {
			if path.Ext(name) != "" {
				c.Status(404)
				return
			}
			name = "index.html"
		}
		b, e := fs.ReadFile(assets, name)
		if e != nil {
			c.String(503, "Frontend not built. Run pnpm --dir web build before building server.")
			return
		}
		http.ServeContent(c.Writer, c.Request, name, time.Time{}, bytes.NewReader(b))
	})
	return r
}
func pagination(c *gin.Context) (int, int) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
func events(c *gin.Context, a *platform.App) {
	if !sameOrigin(c) {
		platform.Fail(c, 403, "ORIGIN_DENIED", nil)
		return
	}
	cursor := c.GetHeader("Last-Event-ID")
	if cursor == "" {
		cursor = c.Query("after")
	}
	after, _ := strconv.ParseInt(cursor, 10, 64)
	c.Header("Content-Type", "text/event-stream")
	c.Header("X-Accel-Buffering", "no")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.WriteHeader(200)
	c.Writer.Flush()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	expires, _ := c.Get("session_expires")
	until, _ := expires.(time.Time)
	for {
		if !until.IsZero() && time.Now().After(until) {
			return
		}
		v, e := a.Tasks.Events(after)
		if e != nil {
			return
		}
		for _, event := range v {
			b, _ := json.Marshal(event)
			if _, e = fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, b); e != nil {
				return
			}
			after = event.ID
		}
		if len(v) > 0 {
			c.Writer.Flush()
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			if _, e = fmt.Fprint(c.Writer, ": heartbeat\n\n"); e != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func Monitor(ctx context.Context, a *platform.App) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	previous := ""
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, e := a.Docker.Ping(c)
		cancel()
		state := "connected"
		if e != nil {
			state = "disconnected"
		}
		if state != previous {
			a.Tasks.Publish("docker.connection", "", gin.H{"host_id": "local", "status": state})
			slog.Info("Docker connection changed", "status", state)
			previous = state
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
