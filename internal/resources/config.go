package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
	"github.com/gin-gonic/gin"
)

type changes struct {
	Image         *string                  `json:"image,omitempty"`
	Env           *[]string                `json:"env,omitempty"`
	Cmd           *[]string                `json:"cmd,omitempty"`
	Entrypoint    *[]string                `json:"entrypoint,omitempty"`
	Labels        *map[string]string       `json:"labels,omitempty"`
	WorkingDir    *string                  `json:"working_dir,omitempty"`
	User          *string                  `json:"user,omitempty"`
	Hostname      *string                  `json:"hostname,omitempty"`
	PortBindings  *nat.PortMap             `json:"port_bindings,omitempty"`
	Mounts        *[]mount.Mount           `json:"mounts,omitempty"`
	Memory        *int64                   `json:"memory,omitempty"`
	NanoCPUs      *int64                   `json:"nano_cpus,omitempty"`
	RestartPolicy *container.RestartPolicy `json:"restart_policy,omitempty"`
}

func (b changes) rebuild() bool {
	return b.Image != nil || b.Env != nil || b.Cmd != nil || b.Entrypoint != nil || b.Labels != nil || b.WorkingDir != nil || b.User != nil || b.Hostname != nil || b.PortBindings != nil || b.Mounts != nil
}
func (b changes) empty() bool {
	return !b.rebuild() && b.Memory == nil && b.NanoCPUs == nil && b.RestartPolicy == nil
}

type configRequest struct {
	Changes     changes `json:"changes"`
	Fingerprint string  `json:"fingerprint"`
	Confirmed   bool    `json:"confirmed"`
}

func fingerprint(v container.InspectResponse) string {
	var networks any
	if v.NetworkSettings != nil {
		networks = v.NetworkSettings.Networks
	}
	var running, paused bool
	if v.State != nil {
		running = v.State.Running
		paused = v.State.Paused
	}
	b, _ := json.Marshal([]any{v.ID, v.Name, v.Image, v.Config, v.HostConfig, v.Mounts, networks, running, paused})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func previewFingerprint(v container.InspectResponse, b changes, desired ...container.InspectResponse) string {
	parts := []any{b}
	for _, d := range desired {
		parts = append(parts, d.Config, d.HostConfig)
	}
	raw, _ := json.Marshal(parts)
	h := sha256.Sum256(append([]byte(fingerprint(v)), raw...))
	return hex.EncodeToString(h[:])
}
func validateRebuild(v container.InspectResponse) error {
	if v.ContainerJSONBase == nil || v.Config == nil || v.HostConfig == nil || v.State == nil {
		return errors.New("incomplete Engine inspect response")
	}
	if v.Config.Labels["com.docker.compose.project"] != "" {
		return errors.New("Compose containers must be edited through their Compose project")
	}
	if v.Config.Labels["com.docker.swarm.service.id"] != "" {
		return errors.New("Swarm service containers cannot be independently rebuilt")
	}
	h := v.HostConfig
	if v.Config.MacAddress != "" {
		return errors.New("explicit container MAC addresses require manual recreation")
	}
	if h.AutoRemove || len(h.Links) > 0 || len(h.VolumesFrom) > 0 || h.ContainerIDFile != "" {
		return errors.New("auto-remove, legacy links, volumes-from and container ID files require manual recreation")
	}
	if strings.HasPrefix(string(h.NetworkMode), "container:") || strings.HasPrefix(string(h.IpcMode), "container:") || strings.HasPrefix(string(h.PidMode), "container:") {
		return errors.New("shared container namespaces cannot be safely rebuilt")
	}
	if v.Platform != "" && v.Platform != "linux" {
		return errors.New("only Linux containers support safe rebuild")
	}
	if v.State.Paused || v.State.Restarting || v.State.Dead {
		return errors.New("container must be running or stopped without an in-progress transition")
	}
	if v.NetworkSettings != nil {
		for _, ep := range v.NetworkSettings.Networks {
			if ep == nil {
				continue
			}
			if len(ep.Links) > 0 {
				return errors.New("legacy endpoint links cannot be safely rebuilt")
			}
			if ep.IPAMConfig != nil && (ep.IPAMConfig.IPv4Address != "" || ep.IPAMConfig.IPv6Address != "" || len(ep.IPAMConfig.LinkLocalIPs) > 0) {
				return errors.New("static network addresses require manual recreation to avoid endpoint conflicts")
			}
		}
	}
	for _, m := range v.Mounts {
		if m.Type != mount.TypeBind && m.Type != mount.TypeVolume && m.Type != mount.TypeTmpfs {
			return fmt.Errorf("unsupported mount type %s", m.Type)
		}
		if m.Type == mount.TypeVolume && m.Name == "" {
			return errors.New("unresolved volume mount")
		}
	}
	return nil
}
func cloneInspect(v container.InspectResponse) (container.InspectResponse, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return v, e
	}
	var out container.InspectResponse
	e = json.Unmarshal(b, &out)
	return out, e
}
func (s *service) prepare(ctx context.Context, v container.InspectResponse, b changes) (container.InspectResponse, error) {
	if b.empty() {
		return v, errors.New("no configuration changes supplied")
	}
	out, e := cloneInspect(v)
	if e != nil {
		return out, e
	}
	if out.Config == nil || out.HostConfig == nil {
		return out, errors.New("incomplete container configuration")
	}
	if out.Config.Labels["com.docker.compose.project"] != "" {
		return out, errors.New("Compose containers must be edited through their Compose project")
	}
	if b.Memory != nil {
		if *b.Memory < 0 {
			return out, errors.New("memory must be non-negative")
		}
		out.HostConfig.Memory = *b.Memory
	}
	if b.NanoCPUs != nil {
		if *b.NanoCPUs < 0 {
			return out, errors.New("nano_cpus must be non-negative")
		}
		out.HostConfig.NanoCPUs = *b.NanoCPUs
	}
	if b.RestartPolicy != nil {
		p := b.RestartPolicy
		if p.Name != "" && p.Name != "no" && p.Name != "always" && p.Name != "unless-stopped" && p.Name != "on-failure" {
			return out, errors.New("invalid restart policy")
		}
		if p.MaximumRetryCount < 0 || (p.Name != "on-failure" && p.MaximumRetryCount != 0) {
			return out, errors.New("retry count is only valid for on-failure")
		}
		out.HostConfig.RestartPolicy = *p
	}
	if !b.rebuild() {
		return out, nil
	}
	if e = validateRebuild(v); e != nil {
		return out, e
	}
	if b.Image != nil {
		if *b.Image == "" {
			return out, errors.New("image is required")
		}
		out.Config.Image = *b.Image
	} else {
		out.Config.Image = v.Image
	}
	resolvedImage, e := s.app.Docker.ImageInspect(ctx, out.Config.Image)
	if e != nil {
		return out, fmt.Errorf("image preflight: %w", e)
	}
	out.Config.Image = resolvedImage.ID
	if b.Env != nil {
		for _, x := range *b.Env {
			if !strings.Contains(x, "=") || strings.HasPrefix(x, "=") {
				return out, errors.New("environment entries must be KEY=value")
			}
		}
		out.Config.Env = *b.Env
	}
	if b.Cmd != nil {
		out.Config.Cmd = *b.Cmd
	}
	if b.Entrypoint != nil {
		out.Config.Entrypoint = *b.Entrypoint
	}
	if b.Labels != nil {
		if (*b.Labels)["com.docker.compose.project"] != "" || (*b.Labels)["com.docker.swarm.service.id"] != "" {
			return out, errors.New("orchestrator ownership labels cannot be set")
		}
		out.Config.Labels = *b.Labels
	}
	if b.WorkingDir != nil {
		out.Config.WorkingDir = *b.WorkingDir
	}
	if b.User != nil {
		out.Config.User = *b.User
	}
	if b.Hostname != nil {
		out.Config.Hostname = *b.Hostname
	}
	if b.PortBindings != nil {
		out.HostConfig.PortBindings = *b.PortBindings
		for p, bindings := range *b.PortBindings {
			if _, e = nat.NewPort(p.Proto(), p.Port()); e != nil {
				return out, e
			}
			for _, binding := range bindings {
				if binding.HostIP != "" && net.ParseIP(binding.HostIP) == nil {
					return out, errors.New("host_ip must be an IP address")
				}
				if binding.HostPort != "" {
					port, e := strconv.Atoi(binding.HostPort)
					if e != nil || port < 0 || port > 65535 {
						return out, errors.New("host_port must be a number from 0 to 65535")
					}
				}
			}
			if out.Config.ExposedPorts == nil {
				out.Config.ExposedPorts = nat.PortSet{}
			}
			out.Config.ExposedPorts[p] = struct{}{}
		}
	}
	if b.PortBindings != nil {
		if e = s.checkPortConflicts(ctx, v.ID, *b.PortBindings); e != nil {
			return out, e
		}
	}
	if b.Mounts != nil {
		for _, m := range *b.Mounts {
			if m.Target == "" || !strings.HasPrefix(m.Target, "/") {
				return out, errors.New("mount target must be absolute")
			}
			switch m.Type {
			case mount.TypeBind:
				if _, e = s.resolveScanPath(m.Source); e != nil {
					return out, fmt.Errorf("new bind mount source: %w", e)
				}
			case mount.TypeVolume:
				if m.Source == "" {
					return out, errors.New("explicit existing volume source is required")
				}
				if _, e = s.app.Docker.VolumeInspect(ctx, m.Source); e != nil {
					return out, e
				}
			case mount.TypeTmpfs:
			default:
				return out, errors.New("unsupported mount type")
			}
		}
		out.HostConfig.Binds = nil
		out.HostConfig.Mounts = *b.Mounts
		out.Config.Volumes = nil
	} else {
		preserveVolumes(&out)
	}
	return out, nil
}

// Inspect contains resolved anonymous volume names that Config.Volumes omits.
func preserveVolumes(v *container.InspectResponse) {
	h := v.HostConfig
	for _, m := range v.Mounts {
		if m.Type != mount.TypeVolume {
			continue
		}
		found := false
		for i, spec := range h.Mounts {
			if spec.Target == m.Destination {
				if spec.Source == "" {
					h.Mounts[i].Source = m.Name
				}
				found = true
				break
			}
		}
		for _, b := range h.Binds {
			parts := strings.Split(b, ":")
			if len(parts) > 1 && parts[1] == m.Destination {
				found = true
				break
			}
		}
		if !found {
			h.Mounts = append(h.Mounts, mount.Mount{Type: mount.TypeVolume, Source: m.Name, Target: m.Destination, ReadOnly: !m.RW})
		}
	}
}
func (s *service) previewConfig(c *gin.Context) {
	var b configRequest
	if !bind(c, &b) {
		return
	}
	v, e := s.app.Docker.ContainerInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	out, e := s.prepare(c.Request.Context(), v, b.Changes)
	if e != nil {
		platform.Fail(c, 422, "CONFIG_UNSUPPORTED", e)
		return
	}
	mode := "update"
	warnings := []string{}
	if b.Changes.rebuild() {
		mode = "rebuild"
		warnings = []string{"Rebuild stops the container and replaces its writable layer; mounted data is retained.", "Rollback restores container configuration and original running state, but cannot undo application data writes.", "The old container is retained until the replacement passes state/health verification."}
	}
	c.JSON(200, gin.H{"fingerprint": previewFingerprint(v, b.Changes, out), "mode": mode, "changes": b.Changes, "before": gin.H{"config": v.Config, "host_config": v.HostConfig}, "after": gin.H{"config": out.Config, "host_config": out.HostConfig}, "warnings": warnings})
}
func (s *service) applyConfig(c *gin.Context) {
	var b configRequest
	if !bind(c, &b) {
		return
	}
	if !b.Confirmed || b.Fingerprint == "" {
		platform.Fail(c, 400, "PREVIEW_CONFIRMATION_REQUIRED", nil)
		return
	}
	v, e := s.app.Docker.ContainerInspect(c.Request.Context(), c.Param("id"))
	if e != nil {
		fail(c, e)
		return
	}
	desired, e := s.prepare(c.Request.Context(), v, b.Changes)
	if e != nil {
		platform.Fail(c, 422, "CONFIG_UNSUPPORTED", e)
		return
	}
	if previewFingerprint(v, b.Changes, desired) != b.Fingerprint {
		platform.Fail(c, 409, "STALE_PREVIEW", errors.New("container, requested changes or resolved image changed; preview again"))
		return
	}
	kind := "container.update"
	if b.Changes.rebuild() {
		kind = "container.rebuild"
	}
	platform.Submit(c, s.app, kind, "container:"+v.ID, false, func(r *task.Run) error {
		current, e := s.app.Docker.ContainerInspect(r.Context(), v.ID)
		if e != nil {
			return e
		}
		out, e := s.prepare(r.Context(), current, b.Changes)
		if e != nil {
			return e
		}
		if previewFingerprint(current, b.Changes, out) != b.Fingerprint {
			return errors.New("container, changes or resolved image changed; preview again")
		}
		if b.Changes.rebuild() {
			return s.rebuild(r, current, out)
		}
		_, e = s.app.Docker.ContainerUpdate(r.Context(), current.ID, container.UpdateConfig{Resources: out.HostConfig.Resources, RestartPolicy: out.HostConfig.RestartPolicy})
		if e != nil {
			return e
		}
		actual, e := s.app.Docker.ContainerInspect(r.Context(), current.ID)
		if e != nil {
			return e
		}
		if actual.HostConfig == nil {
			return errors.New("Engine returned no HostConfig")
		}
		if b.Changes.Memory != nil && actual.HostConfig.Memory != *b.Changes.Memory {
			return errors.New("memory update was not applied")
		}
		if b.Changes.NanoCPUs != nil && actual.HostConfig.NanoCPUs != *b.Changes.NanoCPUs {
			return errors.New("CPU update was not applied")
		}
		if b.Changes.RestartPolicy != nil && actual.HostConfig.RestartPolicy != *b.Changes.RestartPolicy {
			return errors.New("restart policy update was not applied")
		}
		r.Progress("verified", 100)
		return nil
	})
}

type rebuildCheckpoint struct {
	EncryptedOld string `json:"encrypted_old"`
	OriginalName string `json:"original_name"`
	BackupName   string `json:"backup_name"`
	NewID        string `json:"new_id"`
	TaskID       string `json:"task_id"`
	Committed    bool   `json:"committed"`
}

func (s *service) rebuild(r *task.Run, old, out container.InspectResponse) (err error) {
	plain, e := json.Marshal(old)
	if e != nil {
		return e
	}
	encrypted, e := s.app.Vault.Seal(string(plain))
	if e != nil {
		return e
	}
	cp := rebuildCheckpoint{EncryptedOld: encrypted, OriginalName: strings.TrimPrefix(old.Name, "/"), BackupName: strings.TrimPrefix(old.Name, "/") + "-dm-" + r.ID[:min(8, len(r.ID))], TaskID: r.ID}
	if e = r.Checkpoint(cp); e != nil {
		return e
	}
	defer func() {
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if recovery := s.rollback(ctx, cp); recovery != nil {
				err = fmt.Errorf("%w; rollback requires manual intervention: %v", err, recovery)
			}
		}
	}()
	r.Progress("stopping original", 10)
	if old.State.Running {
		if err = s.app.Docker.ContainerStop(r.Context(), old.ID, container.StopOptions{}); err != nil {
			return err
		}
	}
	if err = s.app.Docker.ContainerRename(r.Context(), old.ID, cp.BackupName); err != nil {
		return err
	}
	endpoints := map[string]*network.EndpointSettings{}
	if out.NetworkSettings != nil {
		for name, ep := range out.NetworkSettings.Networks {
			if ep == nil {
				continue
			}
			aliases := []string{}
			for _, a := range ep.Aliases {
				if a != old.ID && a != old.ID[:min(12, len(old.ID))] {
					aliases = append(aliases, a)
				}
			}
			endpoints[name] = &network.EndpointSettings{Aliases: aliases, DriverOpts: ep.DriverOpts, GwPriority: ep.GwPriority}
		}
	}
	if out.Config.Labels == nil {
		out.Config.Labels = map[string]string{}
	}
	out.Config.Labels["io.docker-manager.rebuild-task"] = r.ID
	r.Progress("creating replacement", 40)
	created, e := s.app.Docker.ContainerCreate(r.Context(), out.Config, out.HostConfig, &network.NetworkingConfig{EndpointsConfig: endpoints}, nil, cp.OriginalName)
	if e != nil {
		return e
	}
	cp.NewID = created.ID
	if err = r.Checkpoint(cp); err != nil {
		return err
	}
	if old.State.Running {
		if err = s.app.Docker.ContainerStart(r.Context(), created.ID, container.StartOptions{}); err != nil {
			return err
		}
		if err = s.verifyHealthy(r.Context(), created.ID); err != nil {
			return err
		}
	} else {
		if _, err = s.app.Docker.ContainerInspect(r.Context(), created.ID); err != nil {
			return err
		}
	}
	r.Progress("replacement verified", 90)
	cp.Committed = true
	if err = r.Checkpoint(cp); err != nil {
		cp.Committed = false
		return err
	}
	if e = s.app.Docker.ContainerRemove(r.Context(), old.ID, container.RemoveOptions{RemoveVolumes: false}); e != nil {
		r.Log("Replacement verified; retained stopped original for manual cleanup: " + old.ID)
		return fmt.Errorf("replacement is healthy, but original cleanup failed: %w", e)
	}
	r.Progress("verified", 100)
	return nil
}
func (s *service) verifyHealthy(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	stable := 0
	for {
		v, e := s.app.Docker.ContainerInspect(ctx, id)
		if e != nil {
			return e
		}
		if v.State == nil || !v.State.Running {
			return errors.New("replacement exited before verification")
		}
		if v.State.Health != nil {
			if v.State.Health.Status == "unhealthy" {
				return errors.New("replacement health check failed")
			}
			if v.State.Health.Status == "healthy" {
				return nil
			}
		} else {
			stable++
			if stable >= 3 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("replacement verification: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
func (s *service) recoverRebuild(ctx context.Context, raw json.RawMessage) error {
	var cp rebuildCheckpoint
	if e := json.Unmarshal(raw, &cp); e != nil {
		return e
	}
	return s.rollback(ctx, cp)
}
func (s *service) rollback(ctx context.Context, cp rebuildCheckpoint) error {
	if cp.Committed {
		return nil
	}
	if cp.EncryptedOld == "" {
		return errors.New("missing rebuild checkpoint")
	}
	plain, e := s.app.Vault.Open(cp.EncryptedOld)
	if e != nil {
		return e
	}
	var old container.InspectResponse
	if e = json.Unmarshal([]byte(plain), &old); e != nil {
		return e
	}
	if old.ContainerJSONBase == nil {
		return errors.New("invalid rebuild checkpoint")
	}
	replacement, e := s.app.Docker.ContainerInspect(ctx, cp.OriginalName)
	if e == nil && replacement.ID != old.ID {
		if replacement.Config == nil || replacement.Config.Labels["io.docker-manager.rebuild-task"] != cp.TaskID {
			return errors.New("original name is occupied by an unrelated container")
		}
		if e = s.app.Docker.ContainerRemove(ctx, replacement.ID, container.RemoveOptions{Force: true, RemoveVolumes: false}); e != nil {
			return e
		}
	} else if e != nil && !errdefs.IsNotFound(e) {
		return e
	}
	actual, e := s.app.Docker.ContainerInspect(ctx, old.ID)
	if e != nil {
		return fmt.Errorf("original container is unavailable: %w", e)
	}
	if strings.TrimPrefix(actual.Name, "/") != cp.OriginalName {
		if e = s.app.Docker.ContainerRename(ctx, old.ID, cp.OriginalName); e != nil {
			return e
		}
	}
	if old.State != nil && old.State.Running && (actual.State == nil || !actual.State.Running) {
		if e = s.app.Docker.ContainerStart(ctx, old.ID, container.StartOptions{}); e != nil {
			return e
		}
	}
	return nil
}

func (s *service) checkPortConflicts(ctx context.Context, containerID string, ports nat.PortMap) error {
	all, e := s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		return e
	}
	for _, other := range all {
		if other.ID == containerID || other.State != "running" {
			continue
		}
		for _, existing := range other.Ports {
			for p, bindings := range ports {
				if p.Proto() != existing.Type {
					continue
				}
				for _, binding := range bindings {
					n, _ := strconv.Atoi(binding.HostPort)
					if n != 0 && n == int(existing.PublicPort) && (binding.HostIP == "" || binding.HostIP == "0.0.0.0" || binding.HostIP == "::" || existing.IP == "" || existing.IP == "0.0.0.0" || existing.IP == "::" || binding.HostIP == existing.IP) {
						return fmt.Errorf("host port %d/%s is already used by container %s", n, p.Proto(), other.ID)
					}
				}
			}
		}
	}
	return nil
}
