package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/docker-manager/docker-manager/internal/hostmetrics"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/workflows"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/gin-gonic/gin"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type capability struct {
	Available    bool              `json:"available"`
	Reason       string            `json:"reason"`
	ReasonCode   string            `json:"reason_code,omitempty"`
	ReasonParams map[string]string `json:"reason_params,omitempty"`
}

func capabilities(ctx context.Context, a *platform.App) map[string]capability {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, e := a.Docker.Ping(c)
	docker := capability{Available: true}
	if e != nil {
		docker = capability{Reason: "Docker Engine is unavailable", ReasonCode: "DOCKER_UNAVAILABLE"}
	}
	compose := docker
	if _, e = exec.LookPath(a.Config.ComposePath); e != nil {
		compose = capability{Reason: "Docker Compose v2 CLI is not installed", ReasonCode: "DOCKER_CLI_MISSING", ReasonParams: map[string]string{"tool": a.Config.ComposePath}}
	} else {
		check, stop := context.WithTimeout(ctx, 2*time.Second)
		b, err := exec.CommandContext(check, a.Config.ComposePath, "compose", "version", "--short").Output()
		stop()
		if err != nil || !strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(string(b)), "v"), "2.") {
			compose = capability{Reason: "Docker Compose v2 plugin is unavailable", ReasonCode: "COMPOSE_V2_UNAVAILABLE", ReasonParams: map[string]string{"tool": a.Config.ComposePath}}
		}
	}
	restic := capability{Available: true}
	if _, e = exec.LookPath(a.Config.ResticPath); e != nil {
		restic = capability{Reason: "Restic is not installed", ReasonCode: "RESTIC_MISSING", ReasonParams: map[string]string{"tool": a.Config.ResticPath}}
	}
	daemon := capability{Available: true}
	if err := workflows.DaemonCapability(a); err != nil {
		daemon = capability{Reason: err.Error(), ReasonCode: "DAEMON_CONFIG_INSPECTION_FAILED"}
		var prerequisite *workflows.CapabilityError
		if errors.As(err, &prerequisite) {
			daemon.ReasonCode = prerequisite.Code
			daemon.ReasonParams = prerequisite.Params
		}
	}
	bind := capability{Available: true}
	if len(a.Config.AllowedPaths) == 0 {
		bind = capability{Reason: "Configure explicit allowed paths and container path mappings", ReasonCode: "ALLOWED_PATHS_MISSING"}
	} else if a.Config.Mode == "docker" && len(a.Config.PathMappings) == 0 {
		bind = capability{Reason: "Configure explicit allowed paths and container path mappings", ReasonCode: "PATH_MAPPINGS_MISSING"}
	}
	return map[string]capability{"docker.manage": docker, "compose.manage": compose, "backup.local": restic, "backup.bind_mount": bind, "host.daemon_config": daemon, "host.service_restart": daemon}
}

func dashboard(c *gin.Context, a *platform.App) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	name, _ := os.Hostname()
	cpu := hostmetrics.CPUSnapshot{Status: "unsupported"}
	localHost := hostmetrics.SupportsLocalHost(runtime.GOOS, a.Config.Mode, a.Config.DockerEndpoint)
	if localHost && a.CPU != nil {
		cpu = a.CPU.Snapshot()
	}
	host := gin.H{"hostname": name, "os": runtime.GOOS, "architecture": runtime.GOARCH, "cpu_percent": cpu.Percent, "cpu_sampled_at": cpu.SampledAt, "cpu_status": cpu.Status}
	out := gin.H{"host_id": "local", "docker": gin.H{"connected": false}, "containers": nil, "images": nil, "volumes": nil, "host": host, "backup": gin.H{"latest": nil, "unprotected": nil}, "capabilities": capabilities(ctx, a)}
	respond := func() {
		host["cpu_observed_at"] = time.Now().UTC()
		c.JSON(200, out)
	}
	backup := gin.H{"latest": nil, "unprotected": nil}
	out["backup"] = backup
	var snapshotID, snapshotRef, created string
	if a.DB.QueryRow("SELECT id,snapshot_ref,created_at FROM backup_snapshots WHERE status='verified' ORDER BY created_at DESC LIMIT 1").Scan(&snapshotID, &snapshotRef, &created) == nil {
		backup["latest"] = gin.H{"id": snapshotID, "snapshot_ref": snapshotRef, "created_at": created}
	}
	info, e := a.Docker.Info(ctx)
	if e != nil {
		out["docker"] = gin.H{"connected": false, "error": fmt.Sprintf("Docker connection failed: %v", e)}
		respond()
		return
	}
	version, e := a.Docker.ServerVersion(ctx)
	if e != nil {
		out["docker"] = gin.H{"connected": false, "error": e.Error()}
		respond()
		return
	}
	out["docker"] = gin.H{"connected": true, "info": info, "version": version}
	host["hostname"], host["os"], host["architecture"] = info.Name, info.OperatingSystem, info.Architecture
	host["kernel"], host["cpus"], host["memory_total"], host["docker_root_dir"] = info.KernelVersion, info.NCPU, info.MemTotal, info.DockerRootDir
	host["memory_available"] = nil
	host["load_1m"] = nil
	host["uptime"] = nil
	host["disk"] = nil
	if localHost {
		if b, e := os.ReadFile("/proc/meminfo"); e == nil {
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Fields(line)
				if len(f) > 1 && f[0] == "MemAvailable:" {
					v, _ := strconv.ParseUint(f[1], 10, 64)
					host["memory_available"] = v * 1024
				}
			}
		}
		if b, e := os.ReadFile("/proc/uptime"); e == nil {
			f := strings.Fields(string(b))
			if len(f) > 0 {
				host["uptime"], _ = strconv.ParseFloat(f[0], 64)
			}
		}
		if b, e := os.ReadFile("/proc/loadavg"); e == nil {
			f := strings.Fields(string(b))
			if len(f) > 0 {
				host["load_1m"], _ = strconv.ParseFloat(f[0], 64)
			}
		}
		var st syscall.Statfs_t
		if syscall.Statfs(info.DockerRootDir, &st) == nil {
			host["disk"] = gin.H{"total": uint64(st.Blocks) * uint64(st.Bsize), "available": uint64(st.Bavail) * uint64(st.Bsize), "path": info.DockerRootDir}
		}
	}
	containers, e := a.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		out["resource_error"] = e.Error()
		respond()
		return
	}
	running, stopped, abnormal := 0, 0, 0
	for _, v := range containers {
		switch v.State {
		case "running":
			running++
		case "exited", "created":
			stopped++
		default:
			abnormal++
		}
	}
	out["containers"] = gin.H{"total": len(containers), "running": running, "stopped": stopped, "abnormal": abnormal}
	images, e := a.Docker.ImageList(ctx, image.ListOptions{})
	if e == nil {
		out["images"] = len(images)
	} else {
		out["resource_error"] = e.Error()
	}
	volumes, e := a.Docker.VolumeList(ctx, volume.ListOptions{})
	if e == nil {
		out["volumes"] = len(volumes.Volumes)
		backup["coverage"], backup["unprotected"] = coverage(a, containers, volumes.Volumes)
	} else {
		out["resource_error"] = e.Error()
	}
	diskCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if usage, err := a.Docker.DiskUsage(diskCtx, types.DiskUsageOptions{}); err == nil {
		out["docker_usage"] = usage
	}
	respond()
}

func coverage(a *platform.App, containers []container.Summary, volumes []*volume.Volume) (gin.H, int) {
	type target struct{ Type, Name, Path string }
	type policy struct {
		Enabled bool
		Targets []target
	}
	protectedContainers, protectedVolumes, protectedCompose, protectedPaths := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	rows, e := a.DB.Query("SELECT definition FROM backup_policies")
	if e == nil {
		for rows.Next() {
			var definition string
			var p policy
			if rows.Scan(&definition) == nil && json.Unmarshal([]byte(definition), &p) == nil && p.Enabled {
				for _, t := range p.Targets {
					switch t.Type {
					case "container":
						protectedContainers[t.Name] = true
					case "volume":
						protectedVolumes[t.Name] = true
					case "compose":
						protectedCompose[t.Name] = true
					case "directory":
						protectedPaths[filepath.Clean(t.Path)] = true
					}
				}
			}
		}
		rows.Close()
	}
	projects, e := a.DB.Query("SELECT id,name FROM compose_projects")
	if e == nil {
		for projects.Next() {
			var id, name string
			if projects.Scan(&id, &name) == nil && protectedCompose[id] {
				protectedCompose[name] = true
			}
		}
		projects.Close()
	}
	cp := 0
	for _, c := range containers {
		covered := protectedContainers[c.ID] || protectedCompose[c.Labels["com.docker.compose.project"]]
		for _, name := range c.Names {
			covered = covered || protectedContainers[strings.TrimPrefix(name, "/")]
		}
		if covered {
			cp++
			for _, m := range c.Mounts {
				if m.Type == "volume" {
					protectedVolumes[m.Name] = true
				}
			}
		}
	}
	vp := 0
	for _, v := range volumes {
		if v != nil && protectedVolumes[v.Name] {
			vp++
		}
	}
	return gin.H{"basis": "enabled_backup_policy", "containers": gin.H{"total": len(containers), "protected": cp, "unprotected": len(containers) - cp}, "volumes": gin.H{"total": len(volumes), "protected": vp, "unprotected": len(volumes) - vp}, "directory_policies": len(protectedPaths)}, len(containers) - cp + len(volumes) - vp
}
