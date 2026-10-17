package resources

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type eventWriter struct {
	c              *gin.Context
	pending        []byte
	hideTimestamps bool
}

func (w *eventWriter) Write(p []byte) (int, error) {
	if e := w.c.Request.Context().Err(); e != nil {
		return 0, e
	}
	n := len(p)
	w.pending = append(w.pending, p...)
	for {
		i := strings.IndexByte(string(w.pending), '\n')
		if i < 0 {
			if len(w.pending) > 1024*1024 {
				return 0, errors.New("log line exceeds 1 MiB")
			}
			break
		}
		line := string(w.pending[:i])
		w.pending = w.pending[i+1:]
		if e := w.emit(line); e != nil {
			return 0, e
		}
	}
	return n, nil
}
func (w *eventWriter) emit(line string) error {
	parts := strings.SplitN(line, " ", 2)
	stamp := parts[0]
	id := ""
	if _, e := time.Parse(time.RFC3339Nano, stamp); e == nil {
		id = stamp
		if w.hideTimestamps && len(parts) == 2 {
			line = parts[1]
		}
	}
	b, _ := json.Marshal(gin.H{"text": line})
	if id != "" {
		if _, e := fmt.Fprintf(w.c.Writer, "id: %s\n", id); e != nil {
			return e
		}
	}
	if _, e := fmt.Fprintf(w.c.Writer, "event: log\ndata: %s\n\n", b); e != nil {
		return e
	}
	w.c.Writer.Flush()
	return nil
}
func (s *service) logs(c *gin.Context) {
	tail := c.DefaultQuery("tail", "100")
	n, e := strconv.Atoi(tail)
	if e != nil || n < 0 || n > 10000 {
		platform.Fail(c, 400, "INVALID_TAIL", errors.New("tail must be between 0 and 10000"))
		return
	}
	since := c.Query("since")
	if since == "" {
		since = c.GetHeader("Last-Event-ID")
		if since != "" {
			// Tail limits the initial view, not the reconnect gap. Keeping a small
			// tail here would silently discard lines produced while disconnected.
			tail = "all"
		}
	}
	v, e := s.app.Docker.ContainerInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	stream, e := s.app.Docker.ContainerLogs(c.Request.Context(), v.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: c.DefaultQuery("follow", "true") == "true", Tail: tail, Timestamps: true, Since: since})
	if e != nil {
		fail(c, e)
		return
	}
	defer stream.Close()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Flush()
	w := &eventWriter{c: c, hideTimestamps: c.DefaultQuery("timestamps", "true") != "true"}
	if v.Config != nil && v.Config.Tty {
		_, e = io.Copy(w, stream)
	} else {
		_, e = stdcopy.StdCopy(w, w, stream)
	}
	if len(w.pending) > 0 {
		_ = w.emit(string(w.pending))
	}
	if e != nil && c.Request.Context().Err() == nil {
		b, _ := json.Marshal(gin.H{"error": e.Error()})
		fmt.Fprintf(c.Writer, "event: stream-error\ndata: %s\n\n", b)
		c.Writer.Flush()
	}
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, e := url.Parse(origin)
	if e != nil {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host && (u.Scheme == scheme || r.Header.Get("X-Forwarded-Proto") == u.Scheme)
}
func terminalCSRF(c *gin.Context) bool {
	cookie, e := c.Request.Cookie("csrf_token")
	if e != nil {
		return false
	}
	token := c.Query("csrf_token")
	return token != "" && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) == 1
}
func (s *service) terminal(c *gin.Context) {
	if !sameOrigin(c.Request) || !terminalCSRF(c) {
		platform.Fail(c, 403, "TERMINAL_ORIGIN_OR_CSRF", nil)
		return
	}
	shell := c.DefaultQuery("shell", "/bin/sh")
	if shell != "/bin/sh" && shell != "/bin/bash" && shell != "/bin/ash" {
		platform.Fail(c, 400, "INVALID_SHELL", nil)
		return
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	exec, e := s.app.Docker.ContainerExecCreate(ctx, c.Param("id"), container.ExecOptions{Cmd: []string{shell, "-i"}, Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true})
	if e != nil {
		fail(c, e)
		return
	}
	attach, e := s.app.Docker.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{Tty: true})
	if e != nil {
		platform.Fail(c, 422, "SHELL_UNAVAILABLE", e)
		return
	}
	closeAttach := func() {
		_ = attach.Conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = attach.Conn.Write([]byte("\x03exit\n"))
		_ = attach.CloseWrite()
		attach.Close()
	}
	defer closeAttach()
	up := websocket.Upgrader{CheckOrigin: sameOrigin, ReadBufferSize: 4096, WriteBufferSize: 4096}
	ws, e := up.Upgrade(c.Writer, c.Request, nil)
	if e != nil {
		return
	}
	defer ws.Close()
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		select {
		case <-ctx.Done():
			attach.Close()
			_ = ws.Close()
		case <-sessionDone:
		}
	}()
	ws.SetReadLimit(64 * 1024)
	if deadline, ok := ctx.Deadline(); ok {
		_ = ws.SetReadDeadline(deadline)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ws.Close()
		buf := make([]byte, 8192)
		reader := bufio.NewReader(attach.Reader)
		for {
			n, e := reader.Read(buf)
			if n > 0 {
				_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					return
				}
			}
			if e != nil {
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "terminal closed; shell may be unavailable"), time.Now().Add(time.Second))
				_ = ws.Close()
				return
			}
		}
	}()
	for {
		kind, data, e := ws.ReadMessage()
		if e != nil {
			break
		}
		if kind == websocket.TextMessage {
			var msg struct {
				Type string `json:"type"`
				Rows uint   `json:"rows"`
				Cols uint   `json:"cols"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				if msg.Rows > 0 && msg.Rows <= 1000 && msg.Cols > 0 && msg.Cols <= 1000 {
					_ = s.app.Docker.ContainerExecResize(ctx, exec.ID, container.ResizeOptions{Height: msg.Rows, Width: msg.Cols})
				}
				continue
			}
		}
		_ = attach.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, e = attach.Conn.Write(data); e != nil {
			break
		}
	}
	closeAttach()
	_ = ws.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
