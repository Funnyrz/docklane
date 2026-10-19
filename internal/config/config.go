package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	_ "time/tzdata"

	"github.com/docker-manager/docker-manager/internal/buildinfo"
)

type PathMapping struct {
	Host  string `json:"host"`
	Local string `json:"local"`
}
type Config struct {
	Version           int           `json:"version"`
	LogLevel          string        `json:"log_level"`
	BackupTempDir     string        `json:"backup_temp_dir"`
	Listen            string        `json:"listen"`
	DataDir           string        `json:"data_dir"`
	DockerEndpoint    string        `json:"docker_endpoint"`
	Mode              string        `json:"mode"`
	AllowedPaths      []string      `json:"allowed_paths"`
	PathMappings      []PathMapping `json:"path_mappings"`
	DaemonConfigPath  string        `json:"daemon_config_path"`
	EnableDaemonWrite bool          `json:"enable_daemon_write"`
	SessionSecure     bool          `json:"session_secure"`
	ComposePath       string        `json:"compose_path"`
	ResticPath        string        `json:"restic_path"`
	BackupWorkerImage string        `json:"backup_worker_image"`
}

func Load() (Config, error) {
	c := Config{Version: 1, LogLevel: "info", Listen: ":9999", DataDir: "./data", DockerEndpoint: "unix:///var/run/docker.sock", Mode: "binary", DaemonConfigPath: "/etc/docker/daemon.json", ComposePath: "docker", ResticPath: "restic", BackupWorkerImage: "docklane:v" + buildinfo.Version}
	if p := os.Getenv("DM_CONFIG"); p != "" {
		b, e := os.ReadFile(p)
		if e != nil {
			return c, e
		}
		if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
	}
	for key, dst := range map[string]*string{"DM_LISTEN": &c.Listen, "DM_DATA_DIR": &c.DataDir, "DM_DOCKER_ENDPOINT": &c.DockerEndpoint, "DM_MODE": &c.Mode, "DM_RESTIC_PATH": &c.ResticPath, "DM_COMPOSE_PATH": &c.ComposePath, "DM_BACKUP_WORKER_IMAGE": &c.BackupWorkerImage} {
		if s := os.Getenv(key); s != "" {
			*dst = s
		}
	}
	if s := os.Getenv("DM_ALLOWED_PATHS"); s != "" {
		c.AllowedPaths = strings.Split(s, ",")
	}
	if s := os.Getenv("DM_PATH_MAPPINGS"); s != "" {
		if e := json.Unmarshal([]byte(s), &c.PathMappings); e != nil {
			return c, e
		}
	}
	if os.Getenv("DM_SESSION_SECURE") == "true" {
		c.SessionSecure = true
	}
	if level := os.Getenv("DM_LOG_LEVEL"); level != "" {
		c.LogLevel = level
	}
	if tmp := os.Getenv("DM_BACKUP_TEMP_DIR"); tmp != "" {
		c.BackupTempDir = tmp
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("unsupported log_level %q", c.LogLevel)
	}
	if os.Getenv("DM_ENABLE_DAEMON_WRITE") == "true" {
		c.EnableDaemonWrite = true
	}
	if c.Version != 1 {
		return c, fmt.Errorf("unsupported configuration version %d", c.Version)
	}
	if c.Mode != "binary" && c.Mode != "docker" {
		return c, fmt.Errorf("mode must be binary or docker")
	}
	var e error
	c.DataDir, e = filepath.Abs(c.DataDir)
	if e != nil {
		return c, e
	}
	if c.DataDir == string(filepath.Separator) {
		return c, fmt.Errorf("data_dir cannot be the filesystem root")
	}
	if c.BackupTempDir == "" {
		c.BackupTempDir = filepath.Join(c.DataDir, "tmp")
	}
	c.BackupTempDir, e = filepath.Abs(c.BackupTempDir)
	if e != nil {
		return c, e
	}
	if c.BackupTempDir == string(filepath.Separator) {
		return c, fmt.Errorf("backup_temp_dir cannot be filesystem root")
	}
	for _, root := range c.AllowedPaths {
		if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
			return c, fmt.Errorf("allowed_paths requires explicit absolute directories, never filesystem root")
		}
	}
	for _, mapping := range c.PathMappings {
		if !filepath.IsAbs(mapping.Host) || !filepath.IsAbs(mapping.Local) || filepath.Clean(mapping.Host) == string(filepath.Separator) || filepath.Clean(mapping.Local) == string(filepath.Separator) {
			return c, fmt.Errorf("path mappings must use explicit absolute directories, never filesystem root")
		}
	}
	if c.Mode == "docker" {
		c.EnableDaemonWrite = false
	}
	return c, nil
}
