package api

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"runtime"

	"github.com/docker-manager/docker-manager/internal/buildinfo"
	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/gin-gonic/gin"
)

func daemonToggleSupported(mode string) bool { return runtime.GOOS == "linux" && mode == "binary" }

func settingsResponse(c *gin.Context, a *platform.App, saved config.CapabilitySettings) gin.H {
	effective := config.EffectiveCapabilitySettings(a.Config)
	restartSettings := saved
	// Match the startup deployment guard. A grant retained from a binary-mode
	// database cannot become active in Docker, regardless of further restarts.
	if a.Config.Mode == "docker" {
		restartSettings.EnableDaemonWrite = false
	}
	return gin.H{"version": buildinfo.Version, "mode": a.Config.Mode, "listen": a.Config.Listen, "data_dir": a.Config.DataDir, "allowed_paths": effective.AllowedPaths, "path_mappings": effective.PathMappings, "capabilities": capabilities(c.Request.Context(), a), "capability_settings": gin.H{"effective": effective, "saved": saved, "pending_restart": !reflect.DeepEqual(effective, restartSettings), "daemon_toggle_supported": daemonToggleSupported(a.Config.Mode)}}
}

func getSettings(c *gin.Context, a *platform.App) {
	saved, err := config.SavedCapabilitySettings(a.DB, a.Config)
	if err != nil {
		platform.Fail(c, 500, "SETTINGS_READ_FAILED", nil)
		return
	}
	c.JSON(200, settingsResponse(c, a, saved))
}

func updateCapabilitySettings(c *gin.Context, a *platform.App, authn *auth) {
	var in struct {
		AllowedPaths      *[]string             `json:"allowed_paths"`
		PathMappings      *[]config.PathMapping `json:"path_mappings"`
		EnableDaemonWrite *bool                 `json:"enable_daemon_write"`
		Confirm           *bool                 `json:"confirm"`
		Password          *string               `json:"password"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || in.AllowedPaths == nil || in.PathMappings == nil || in.EnableDaemonWrite == nil || in.Confirm == nil || !*in.Confirm || in.Password == nil || *in.Password == "" {
		platform.Fail(c, 400, "INVALID_INPUT", nil)
		return
	}
	limitKey := "settings:" + platform.Actor(c)
	if !authn.allowed(limitKey) {
		c.Header("Retry-After", "900")
		platform.Fail(c, 429, "LOGIN_RATE_LIMIT", nil)
		return
	}
	var hash string
	if err := a.DB.QueryRowContext(c.Request.Context(), "SELECT password_hash FROM users WHERE id=?", platform.Actor(c)).Scan(&hash); err != nil || !security.CheckPassword(hash, *in.Password) {
		platform.Fail(c, 403, "PASSWORD_CONFIRMATION_FAILED", nil)
		return
	}
	authn.mu.Lock()
	delete(authn.attempts, limitKey)
	authn.mu.Unlock()
	saved, err := config.NormalizeCapabilitySettings(config.CapabilitySettings{AllowedPaths: *in.AllowedPaths, PathMappings: *in.PathMappings, EnableDaemonWrite: *in.EnableDaemonWrite}, a.Config.Mode)
	if err != nil {
		var validation *config.SettingsValidationError
		if errors.As(err, &validation) {
			platform.Fail(c, 400, validation.Code, nil)
		} else {
			platform.Fail(c, 400, "INVALID_INPUT", nil)
		}
		return
	}
	if saved.EnableDaemonWrite && !daemonToggleSupported(a.Config.Mode) {
		platform.Fail(c, 400, "SETTINGS_DAEMON_UNSUPPORTED", nil)
		return
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		platform.Fail(c, 500, "SETTINGS_SAVE_FAILED", nil)
		return
	}
	tx, err := a.DB.BeginTx(c.Request.Context(), nil)
	if err != nil {
		platform.Fail(c, 500, "SETTINGS_SAVE_FAILED", nil)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(c.Request.Context(), "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", config.CapabilitySettingsKey, string(raw))
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), "INSERT INTO audit_logs VALUES(?,?,?,?,?,?,?)", database.ID(), platform.Actor(c), "settings.capabilities.update", "settings", config.CapabilitySettingsKey, "Succeeded", database.Now())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		platform.Fail(c, 500, "SETTINGS_SAVE_FAILED", nil)
		return
	}
	c.JSON(200, settingsResponse(c, a, saved))
}
