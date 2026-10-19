package config

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const CapabilitySettingsKey = "capability_settings_v1"

// CapabilitySettings is the deliberately small set administrators may edit in the UI.
// Running services keep their Config snapshot until the next process startup.
type CapabilitySettings struct {
	AllowedPaths      []string      `json:"allowed_paths"`
	PathMappings      []PathMapping `json:"path_mappings"`
	EnableDaemonWrite bool          `json:"enable_daemon_write"`
}

type SettingsValidationError struct{ Code string }

func (e *SettingsValidationError) Error() string { return e.Code }

func EffectiveCapabilitySettings(c Config) CapabilitySettings {
	return CapabilitySettings{append([]string{}, c.AllowedPaths...), append([]PathMapping{}, c.PathMappings...), c.EnableDaemonWrite}
}

// NormalizeCapabilitySettings checks grants without creating or modifying paths.
// Host paths in Docker mode belong to another namespace; only local paths can
// be checked for symlinks here. Resource operations still resolve paths again.
func NormalizeCapabilitySettings(in CapabilitySettings, mode string) (CapabilitySettings, error) {
	out := CapabilitySettings{AllowedPaths: []string{}, PathMappings: []PathMapping{}, EnableDaemonWrite: in.EnableDaemonWrite}
	if len(in.AllowedPaths) > 128 || len(in.PathMappings) > 128 {
		return out, &SettingsValidationError{"SETTINGS_LIMIT_EXCEEDED"}
	}
	allowed, hosts, locals := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range in.AllowedPaths {
		clean, err := explicitDirectory(p, mode != "docker")
		if err != nil || allowed[clean] {
			return out, &SettingsValidationError{"SETTINGS_INVALID_PATH"}
		}
		allowed[clean] = true
		out.AllowedPaths = append(out.AllowedPaths, clean)
	}
	for _, m := range in.PathMappings {
		host, hostErr := explicitDirectory(m.Host, mode != "docker")
		local, localErr := explicitDirectory(m.Local, true)
		if hostErr != nil || localErr != nil || hosts[host] || locals[local] {
			return out, &SettingsValidationError{"SETTINGS_INVALID_MAPPING"}
		}
		hosts[host], locals[local] = true, true
		out.PathMappings = append(out.PathMappings, PathMapping{Host: host, Local: local})
	}
	return out, nil
}

func explicitDirectory(p string, local bool) (string, error) {
	if strings.ContainsRune(p, 0) || !filepath.IsAbs(p) {
		return "", errors.New("an absolute directory is required")
	}
	clean := filepath.Clean(p)
	if clean == string(filepath.Separator) {
		return "", errors.New("filesystem root cannot be authorized")
	}
	if local {
		resolved, err := filepath.EvalSymlinks(clean)
		if err == nil && filepath.Clean(resolved) == string(filepath.Separator) {
			return "", errors.New("filesystem root cannot be authorized through a symlink")
		}
		if err != nil && !os.IsNotExist(err) {
			return "", errors.New("directory cannot be inspected")
		}
		if info, err := os.Stat(clean); err == nil && !info.IsDir() {
			return "", errors.New("path is not a directory")
		}
	}
	return clean, nil
}

// DecodeCapabilitySettings rejects incomplete or future schemas rather than
// silently discarding a saved authorization on restart.
func DecodeCapabilitySettings(data []byte, mode string) (CapabilitySettings, error) {
	var in struct {
		AllowedPaths      *[]string      `json:"allowed_paths"`
		PathMappings      *[]PathMapping `json:"path_mappings"`
		EnableDaemonWrite *bool          `json:"enable_daemon_write"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return CapabilitySettings{}, err
	}
	if decoder.Decode(new(any)) != io.EOF || in.AllowedPaths == nil || in.PathMappings == nil || in.EnableDaemonWrite == nil {
		return CapabilitySettings{}, errors.New("incomplete capability settings")
	}
	return NormalizeCapabilitySettings(CapabilitySettings{*in.AllowedPaths, *in.PathMappings, *in.EnableDaemonWrite}, mode)
}

func SavedCapabilitySettings(db *sql.DB, c Config) (CapabilitySettings, error) {
	var raw string
	err := db.QueryRow("SELECT value FROM settings WHERE key=?", CapabilitySettingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return EffectiveCapabilitySettings(c), nil
	}
	if err != nil {
		return CapabilitySettings{}, err
	}
	return DecodeCapabilitySettings([]byte(raw), c.Mode)
}

// ApplySavedCapabilitySettings runs once, before services and task recovery start.
// Saved fields deliberately override the corresponding environment/file fields.
func ApplySavedCapabilitySettings(db *sql.DB, c Config) (Config, error) {
	saved, err := SavedCapabilitySettings(db, c)
	if err != nil {
		return c, fmt.Errorf("saved capability settings are invalid: %w", err)
	}
	c.AllowedPaths, c.PathMappings, c.EnableDaemonWrite = saved.AllowedPaths, saved.PathMappings, saved.EnableDaemonWrite
	if c.Mode == "docker" {
		c.EnableDaemonWrite = false
	}
	return c, nil
}
