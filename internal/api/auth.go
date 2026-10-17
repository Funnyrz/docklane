package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type auth struct {
	app      *platform.App
	mu       sync.Mutex
	attempts map[string][]time.Time
}
type user struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (a *auth) initialized() bool {
	var n int
	_ = a.app.DB.QueryRow("SELECT count(*) FROM users").Scan(&n)
	return n > 0
}
func (a *auth) current(c *gin.Context) (user, string, time.Time, error) {
	token, e := c.Cookie("dm_session")
	if e != nil {
		return user{}, "", time.Time{}, e
	}
	var u user
	var csrf, expiry string
	e = a.app.DB.QueryRow("SELECT u.id,u.username,s.csrf_token,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?", security.TokenHash(token)).Scan(&u.ID, &u.Username, &csrf, &expiry)
	if e != nil {
		return u, "", time.Time{}, e
	}
	until, e := time.Parse(time.RFC3339Nano, expiry)
	if e != nil || !time.Now().Before(until) {
		return user{}, "", time.Time{}, errors.New("session expired")
	}
	return u, csrf, until, nil
}
func (a *auth) cookie(c *gin.Context, name, value string, maxAge int, httpOnly bool) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(name, value, maxAge, "/", "", a.app.Config.SessionSecure, httpOnly)
}
func sameOrigin(c *gin.Context) bool {
	origin := c.GetHeader("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == c.Request.Host
}
func (a *auth) csrf(c *gin.Context) bool {
	if !sameOrigin(c) {
		platform.Fail(c, 403, "ORIGIN_DENIED", nil)
		return false
	}
	cookie, e := c.Cookie("csrf_token")
	header := c.GetHeader("X-CSRF-Token")
	if e != nil || len(cookie) != 43 || subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
		platform.Fail(c, 403, "CSRF_INVALID", nil)
		return false
	}
	return true
}
func (a *auth) status(c *gin.Context) {
	u, csrf, _, e := a.current(c)
	out := gin.H{"initialized": a.initialized(), "authenticated": e == nil}
	if e == nil {
		out["user"] = u
	} else {
		csrf, _ = c.Cookie("csrf_token")
		if len(csrf) != 43 {
			csrf = security.Token()
		}
	}
	a.cookie(c, "csrf_token", csrf, 8*3600, false)
	out["csrf_token"] = csrf
	c.JSON(200, out)
}
func (a *auth) session(c *gin.Context, u user) {
	token, csrf := security.Token(), security.Token()
	_, e := a.app.DB.Exec("INSERT INTO sessions VALUES(?,?,?,?)", security.TokenHash(token), u.ID, csrf, time.Now().Add(8*time.Hour).UTC().Format(time.RFC3339Nano))
	if e != nil {
		platform.Fail(c, 500, "SESSION_FAILED", nil)
		return
	}
	a.cookie(c, "dm_session", token, 8*3600, true)
	a.cookie(c, "csrf_token", csrf, 8*3600, false)
	c.JSON(200, gin.H{"initialized": true, "authenticated": true, "user": u, "csrf_token": csrf})
}
func (a *auth) setup(c *gin.Context) {
	if !a.csrf(c) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		platform.Fail(c, 400, "INVALID_INPUT", nil)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if len(in.Username) < 1 || len(in.Username) > 64 {
		platform.Fail(c, 400, "INVALID_USERNAME", nil)
		return
	}
	// Serialize initialization inside the database; checking outside a transaction races.
	tx, e := a.app.DB.BeginTx(c.Request.Context(), nil)
	if e != nil {
		platform.Fail(c, 500, "DATABASE_ERROR", nil)
		return
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow("SELECT count(*) FROM users").Scan(&n); e != nil {
		platform.Fail(c, 500, "DATABASE_ERROR", nil)
		return
	}
	if n > 0 {
		platform.Fail(c, 409, "ALREADY_INITIALIZED", nil)
		return
	}
	h, e := security.HashPassword(in.Password)
	if e != nil {
		platform.Fail(c, 400, "WEAK_PASSWORD", e)
		return
	}
	u := user{database.ID(), in.Username}
	_, e = tx.Exec("INSERT INTO users VALUES(?,?,?,?)", u.ID, u.Username, h, database.Now())
	if e == nil {
		_, e = tx.Exec("INSERT INTO audit_logs VALUES(?,?,?,?,?,?,?)", database.ID(), u.ID, "auth.setup", "user", u.ID, "Succeeded", database.Now())
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		platform.Fail(c, 500, "INITIALIZATION_FAILED", nil)
		return
	}
	a.session(c, u)
}
func (a *auth) allowed(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for key, list := range a.attempts {
		filtered := list[:0]
		for _, t := range list {
			if now.Sub(t) < 15*time.Minute {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			delete(a.attempts, key)
		} else {
			a.attempts[key] = filtered
		}
	}
	if len(a.attempts) >= 10000 {
		return false
	}
	list := a.attempts[ip]
	if len(list) >= 10 {
		return false
	}
	a.attempts[ip] = append(list, now)
	return true
}
func (a *auth) login(c *gin.Context) {
	if !a.csrf(c) {
		return
	}
	if !a.allowed(c.ClientIP()) {
		c.Header("Retry-After", "900")
		platform.Fail(c, 429, "LOGIN_RATE_LIMIT", nil)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if c.ShouldBindJSON(&in) != nil {
		platform.Fail(c, 400, "INVALID_INPUT", nil)
		return
	}
	var u user
	var hash string
	e := a.app.DB.QueryRow("SELECT id,username,password_hash FROM users WHERE username=?", in.Username).Scan(&u.ID, &u.Username, &hash)
	if e != nil {
		hash = "argon2id$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	valid := security.CheckPassword(hash, in.Password)
	if e != nil || !valid {
		_ = database.Audit(a.app.DB, "anonymous", "auth.login", "user", "", "Failed")
		platform.Fail(c, 401, "INVALID_CREDENTIALS", nil)
		return
	}
	a.mu.Lock()
	delete(a.attempts, c.ClientIP())
	a.mu.Unlock()
	_ = database.Audit(a.app.DB, u.ID, "auth.login", "user", u.ID, "Succeeded")
	a.session(c, u)
}
func (a *auth) require(c *gin.Context) {
	u, csrf, expires, e := a.current(c)
	if e != nil {
		platform.Fail(c, 401, "AUTH_REQUIRED", nil)
		return
	}
	c.Set("actor", u.ID)
	c.Set("user", u)
	c.Set("session_expires", expires)
	ctx, cancel := context.WithDeadline(c.Request.Context(), expires)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	// Revoke long-lived event/log/terminal connections after logout as well as expiry.
	if strings.HasSuffix(c.Request.URL.Path, "/events") || strings.HasSuffix(c.Request.URL.Path, "/logs") || strings.HasSuffix(c.Request.URL.Path, "/stats") || strings.HasSuffix(c.Request.URL.Path, "/terminal") {
		token, _ := c.Cookie("dm_session")
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					var exists int
					if a.app.DB.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE token_hash=?", security.TokenHash(token)).Scan(&exists) != nil || exists == 0 {
						cancel()
						return
					}
				}
			}
		}()
	}
	if id := c.Param("host_id"); id != "" && id != "local" {
		platform.Fail(c, 404, "HOST_NOT_FOUND", nil)
		return
	}
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.Request.Method != "OPTIONS" {
		if !a.csrf(c) {
			return
		}
		if subtle.ConstantTimeCompare([]byte(csrf), []byte(c.GetHeader("X-CSRF-Token"))) != 1 {
			platform.Fail(c, 403, "CSRF_INVALID", nil)
			return
		}
	}
	if !a.app.Tasks.Ready() && ((c.Request.Method != "GET" && c.Request.Method != "HEAD") || strings.HasSuffix(c.Request.URL.Path, "/terminal")) && c.Request.URL.Path != "/api/v1/auth/logout" && !strings.HasSuffix(c.FullPath(), "/tasks/:id/cancel") {
		platform.Fail(c, 503, "RECOVERY_REQUIRED", errors.New("an interrupted operation still needs compensation; writes are paused while recovery retries"))
		return
	}
	c.Next()
}
func (a *auth) logout(c *gin.Context) {
	t, _ := c.Cookie("dm_session")
	_, e := a.app.DB.Exec("DELETE FROM sessions WHERE token_hash=?", security.TokenHash(t))
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		platform.Fail(c, 500, "DATABASE_ERROR", nil)
		return
	}
	a.cookie(c, "dm_session", "", -1, true)
	a.cookie(c, "csrf_token", "", -1, false)
	c.JSON(200, gin.H{"authenticated": false})
}
