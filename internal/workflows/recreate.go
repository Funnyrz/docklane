package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker-manager/docker-manager/internal/safepath"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
)

type restoreContainer struct {
	Name       string        `json:"name"`
	Image      string        `json:"image"`
	OriginalID string        `json:"original_id"`
	Mounts     []mount.Mount `json:"mounts"`
	config     *container.Config
	host       *container.HostConfig
	networks   *network.NetworkingConfig
}
type resourcePlan struct {
	Containers []restoreContainer `json:"containers"`
	Projects   []Project          `json:"projects"`
	Warning    string             `json:"warning"`
}

func (s *Service) recoveredPath(req restoreRequest, m Manifest, host string) (string, error) {
	if e := validateManifest(m); e != nil {
		return "", e
	}
	if !strings.HasPrefix(host, "volume:") {
		host = filepath.Clean(host)
	}
	for _, src := range m.Sources {
		match := src.HostPath
		if src.Volume != "" {
			match = "volume:" + src.Volume
		}
		if host == match || src.Volume == "" && strings.HasPrefix(host, strings.TrimRight(match, "/")+"/") {
			rel := ""
			if src.Volume == "" {
				rel = strings.TrimPrefix(strings.TrimPrefix(host, match), "/")
			}
			return filepath.Join(req.Target, strings.TrimPrefix(src.SnapshotPath, "/"), rel), nil
		}
	}
	return "", fmt.Errorf("mount/config path %s was not included in this backup", host)
}
func selected(ids []string, id string) bool {
	if ids == nil {
		return true
	}
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func (s *Service) resourcePreflight(ctx context.Context, req restoreRequest, m Manifest) (resourcePlan, error) {
	plan := resourcePlan{Containers: []restoreContainer{}, Projects: []Project{}, Warning: "Restored volumes are explicitly rebound to the recovered files below the new target. Service health is checked separately after file verification. Existing resources are never overwritten by recreation."}
	if !req.Recreate {
		return plan, nil
	}
	if req.Overwrite {
		return plan, errors.New("service recreation requires a new restore directory; overwrite and recreation cannot be combined")
	}
	if s.app.Config.Mode == "docker" {
		for _, mapping := range s.app.Config.PathMappings {
			if mapping.Host != mapping.Local {
				return plan, errors.New("service recreation requires identical host/container mappings so restored Compose bind paths remain valid")
			}
		}
	}
	live, e := s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if e != nil {
		return plan, e
	}
	names := map[string]bool{}
	ports := map[string]bool{}
	projects := map[string]bool{}
	for _, ct := range live {
		for _, n := range ct.Names {
			names[strings.TrimPrefix(n, "/")] = true
		}
		projects[ct.Labels["com.docker.compose.project"]] = true
		for _, p := range ct.Ports {
			if p.PublicPort > 0 {
				ports[fmt.Sprintf("%d/%s", p.PublicPort, p.Type)] = true
			}
		}
	}
	selectedProjects := map[string]bool{}
	for _, p := range m.Compose {
		p.ConfigPaths = append([]string{}, p.ConfigPaths...)
		p.EnvFiles = append([]string{}, p.EnvFiles...)
		p.Profiles = append([]string{}, p.Profiles...)
		if !selected(req.ProjectIDs, p.ID) {
			continue
		}
		var compiled struct {
			Services map[string]struct {
				ContainerName string         `json:"container_name"`
				DependsOn     map[string]any `json:"depends_on"`
			} `json:"services"`
			Networks map[string]struct {
				Name     string `json:"name"`
				External bool   `json:"external"`
			} `json:"networks"`
		}
		rawConfig := m.ComposeConfig[p.ID]
		if len(rawConfig) == 0 {
			return plan, errors.New("Compose snapshot lacks compiled configuration; recover files and review/import manually")
		}
		if raw := rawConfig; len(raw) > 0 {
			if e = json.Unmarshal(raw, &compiled); e != nil {
				return plan, e
			}
			captured := map[string]bool{}
			for _, raw := range m.Containers {
				var ct container.InspectResponse
				if e = json.Unmarshal(raw, &ct); e != nil {
					return plan, e
				}
				if ct.Config != nil && ct.Config.Labels["com.docker.compose.project"] == p.Name {
					captured[ct.Config.Labels["com.docker.compose.service"]] = true
				}
			}
			if len(captured) == 0 {
				return plan, errors.New("Compose snapshot has no captured runtime services; recover files and deploy reviewed configuration manually")
			}
			for serviceName, service := range compiled.Services {
				if !captured[serviceName] {
					continue
				}
				for dependency := range service.DependsOn {
					if !captured[dependency] {
						return plan, fmt.Errorf("captured service %s depends on uncaptured service %s; review manually", serviceName, dependency)
					}
				}
				if service.ContainerName != "" && names[service.ContainerName] {
					return plan, fmt.Errorf("explicit Compose container_name %s already exists", service.ContainerName)
				}
			}
			for key, networkConfig := range compiled.Networks {
				if networkConfig.External {
					externalName := networkConfig.Name
					if externalName == "" {
						externalName = key
					}
					if _, e = s.app.Docker.NetworkInspect(ctx, externalName, network.InspectOptions{}); e != nil {
						return plan, fmt.Errorf("external Compose network %s unavailable", externalName)
					}
				}
			}
		}
		original := p.Workdir
		work, e := s.recoveredPath(req, m, original)
		if e != nil {
			return plan, e
		}
		p.Workdir = work
		newName := req.ProjectNames[p.ID]
		if newName == "" {
			newName = p.Name + "-restored-" + req.Snapshot[:min(8, len(req.Snapshot))]
		}
		if projects[newName] {
			return plan, errors.New("restored Compose project name already exists")
		}
		p.Name = newName
		p.ID = ""
		for i, f := range p.ConfigPaths {
			if !filepath.IsAbs(f) {
				f = filepath.Join(original, f)
			}
			p.ConfigPaths[i], e = s.recoveredPath(req, m, f)
			if e != nil {
				return plan, e
			}
		}
		for i, f := range p.EnvFiles {
			if !filepath.IsAbs(f) {
				f = filepath.Join(original, f)
			}
			p.EnvFiles[i], e = s.recoveredPath(req, m, f)
			if e != nil {
				return plan, e
			}
		}
		p.Source = "recovered"
		p.Revision = ""
		plan.Projects = append(plan.Projects, p)
	}
	// Identify original Compose ownership independently of copied project names.
	selectedProjects = map[string]bool{}
	for _, p := range m.Compose {
		if selected(req.ProjectIDs, p.ID) {
			selectedProjects[p.Name] = true
		}
	}
	for _, raw := range m.Containers {
		var old container.InspectResponse
		if e = json.Unmarshal(raw, &old); e != nil {
			return plan, e
		}
		if old.Config == nil || old.HostConfig == nil || old.ContainerJSONBase == nil {
			return plan, errors.New("container manifest is incomplete")
		}
		project := old.Config.Labels["com.docker.compose.project"]
		isProject := project != "" && selectedProjects[project]
		if !isProject && !selected(req.ContainerIDs, old.ID) {
			continue
		}
		if project != "" && !isProject {
			return plan, errors.New("Compose containers must be recreated through their original project")
		}
		for port, bindings := range old.HostConfig.PortBindings {
			for _, binding := range bindings {
				if binding.HostPort != "" && ports[binding.HostPort+"/"+port.Proto()] {
					return plan, fmt.Errorf("published port %s/%s conflicts with an existing service", binding.HostPort, port.Proto())
				}
			}
		}
		if old.HostConfig.Privileged || old.HostConfig.ContainerIDFile != "" || len(old.HostConfig.Devices) > 0 || len(old.HostConfig.DeviceRequests) > 0 || len(old.HostConfig.VolumesFrom) > 0 || len(old.HostConfig.Links) > 0 || strings.HasPrefix(string(old.HostConfig.NetworkMode), "container:") || strings.HasPrefix(string(old.HostConfig.PidMode), "container:") || strings.HasPrefix(string(old.HostConfig.IpcMode), "container:") {
			return plan, errors.New("special privilege/device/shared namespace configuration requires manual recreation")
		}
		for _, mp := range old.Mounts {
			if mp.Type == mount.TypeTmpfs {
				continue
			}
			if mp.Propagation != "" && mp.Propagation != "rprivate" || strings.Contains(mp.Mode, "z") || strings.Contains(mp.Mode, "Z") {
				return plan, errors.New("custom bind propagation/SELinux mount modes require manual recreation")
			}
			source := mp.Source
			if mp.Type == mount.TypeVolume {
				source = "volume:" + mp.Name
			}
			if _, e = s.recoveredPath(req, m, source); e != nil {
				return plan, e
			}
		}
		if isProject {
			if _, e = s.app.Docker.ImageInspect(ctx, old.Image); e != nil {
				return plan, fmt.Errorf("original Compose image unavailable; import or pull its original digest: %w", e)
			}
			continue
		}
		name := req.ContainerNames[old.ID]
		if name == "" {
			name = strings.TrimPrefix(old.Name, "/")
		}
		if names[name] {
			return plan, fmt.Errorf("container name %s exists; choose a new container_names mapping", name)
		}
		names[name] = true
		if _, e = s.app.Docker.ImageInspect(ctx, old.Image); e != nil {
			return plan, fmt.Errorf("original image unavailable; import/pull the original digest before recreation: %w", e)
		}
		config := old.Config
		config.Image = old.Image
		host := old.HostConfig
		host.Binds = nil
		host.Mounts = nil
		mounts := []mount.Mount{}
		for _, mp := range old.Mounts {
			if mp.Type == mount.TypeTmpfs {
				continue
			}
			source := mp.Source
			if mp.Type == mount.TypeVolume {
				source = "volume:" + mp.Name
			}
			dest, e := s.recoveredPath(req, m, source)
			if e != nil {
				return plan, e
			}
			mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: dest, Target: mp.Destination, ReadOnly: !mp.RW})
		}
		host.Mounts = mounts
		networking := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
		if old.NetworkSettings != nil {
			for name, ep := range old.NetworkSettings.Networks {
				if ep.IPAMConfig != nil {
					return plan, errors.New("static network addressing needs manual recreation")
				}
				if _, e = s.app.Docker.NetworkInspect(ctx, name, network.InspectOptions{}); e != nil {
					return plan, fmt.Errorf("required network %s unavailable", name)
				}
				if len(ep.Links) > 0 {
					return plan, errors.New("legacy network links require manual recreation")
				}
				networking.EndpointsConfig[name] = &network.EndpointSettings{Aliases: ep.Aliases, DriverOpts: ep.DriverOpts, GwPriority: ep.GwPriority}
			}
		}
		plan.Containers = append(plan.Containers, restoreContainer{Name: name, Image: old.Image, OriginalID: old.ID, Mounts: mounts, config: config, host: host, networks: networking})
	}
	return plan, nil
}
func (s *Service) recreateResources(t *task.Run, req restoreRequest, m Manifest, cp *stopCheckpoint) error {
	plan, e := s.resourcePreflight(t.Context(), req, m)
	if e != nil {
		return e
	}
	cp.RecoveryTask = t.ID
	if e = t.Checkpoint(cp); e != nil {
		return e
	}
	for _, p := range plan.Projects {
		originalID := ""
		originalName := ""
		for _, original := range m.Compose {
			if !selected(req.ProjectIDs, original.ID) {
				continue
			}
			name := req.ProjectNames[original.ID]
			if name == "" {
				name = original.Name + "-restored-" + req.Snapshot[:min(8, len(req.Snapshot))]
			}
			if name == p.Name {
				originalID = original.ID
				originalName = original.Name
				break
			}
		}
		var compiled map[string]any
		if e = json.Unmarshal(m.ComposeConfig[originalID], &compiled); e != nil {
			return e
		}
		allServices, _ := compiled["services"].(map[string]any)
		recoveredServices := map[string]any{}
		serviceNames := []string{}
		for _, raw := range m.Containers {
			var old container.InspectResponse
			if e = json.Unmarshal(raw, &old); e != nil {
				return e
			}
			if old.Config == nil || old.Config.Labels["com.docker.compose.project"] != originalName {
				continue
			}
			name := old.Config.Labels["com.docker.compose.service"]
			service, _ := allServices[name].(map[string]any)
			if service == nil {
				return errors.New("captured Compose service is missing from compiled configuration")
			}
			volumes := []map[string]any{}
			for _, mp := range old.Mounts {
				if mp.Type == mount.TypeTmpfs {
					continue
				}
				source := mp.Source
				if mp.Type == mount.TypeVolume {
					source = "volume:" + mp.Name
				}
				recovered, e := s.recoveredPath(req, m, source)
				if e != nil {
					return e
				}
				volumes = append(volumes, map[string]any{"type": "bind", "source": recovered, "target": mp.Destination, "read_only": !mp.RW})
			}
			service["volumes"] = volumes
			service["image"] = old.Image
			delete(service, "build")
			delete(service, "env_file")
			labels, _ := service["labels"].(map[string]any)
			if labels == nil {
				labels = map[string]any{}
			}
			labels["docker-manager.recovery"] = t.ID
			service["labels"] = labels
			if _, exists := recoveredServices[name]; !exists {
				serviceNames = append(serviceNames, name)
			}
			recoveredServices[name] = service
		}
		if len(serviceNames) == 0 {
			return errors.New("Compose snapshot has no captured services to recreate")
		}
		compiled["services"] = recoveredServices
		for _, kind := range []string{"configs", "secrets"} {
			entries, _ := compiled[kind].(map[string]any)
			for _, value := range entries {
				entry, _ := value.(map[string]any)
				if source, ok := entry["file"].(string); ok {
					entry["file"], e = s.recoveredPath(req, m, source)
					if e != nil {
						return e
					}
				}
			}
		}
		if networks, ok := compiled["networks"].(map[string]any); ok {
			for key, value := range networks {
				networkConfig, _ := value.(map[string]any)
				external, _ := networkConfig["external"].(bool)
				if !external {
					networkConfig["name"] = p.Name + "_" + key
				}
			}
		}
		compiled["name"] = p.Name
		recoveryFile := filepath.Join(p.Workdir, ".docker-manager-recovery.json")
		local, e := s.path(recoveryFile)
		if e != nil {
			return e
		}
		encoded := strings.ReplaceAll(jsonString(compiled), "$", "$$")
		if e = safepath.AtomicWrite(local, []byte(encoded), 0600); e != nil {
			return e
		}
		p.ConfigPaths = []string{recoveryFile}
		p.EnvFiles = nil
		p.Environment = nil
		if _, e = s.compose(t.Context(), p, "config", "--quiet"); e != nil {
			return e
		}
		sort.Strings(serviceNames)
		args := append([]string{"up", "--detach", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "120"}, serviceNames...)
		if _, e = s.compose(t.Context(), p, args...); e != nil {
			return e
		}
		cts, e := s.app.Docker.ContainerList(t.Context(), container.ListOptions{All: true})
		if e != nil {
			return e
		}
		seenServices := map[string]bool{}
		for _, ct := range cts {
			if ct.Labels["docker-manager.recovery"] == t.ID && ct.Labels["com.docker.compose.project"] == p.Name {
				seenServices[ct.Labels["com.docker.compose.service"]] = true
				cp.Created = append(cp.Created, ct.ID)
				if e = t.Checkpoint(cp); e != nil {
					return e
				}
				if e = s.waitServiceStable(t.Context(), ct.ID); e != nil {
					return e
				}
			}
		}
	}

	for _, it := range plan.Containers {
		if it.config.Labels == nil {
			it.config.Labels = map[string]string{}
		}
		it.config.Labels["docker-manager.recovery"] = t.ID
		created, e := s.app.Docker.ContainerCreate(t.Context(), it.config, it.host, it.networks, nil, it.Name)
		if e != nil {
			return e
		}
		cp.Created = append(cp.Created, created.ID)
		if e = t.Checkpoint(cp); e != nil {
			return e
		}
		if e = s.app.Docker.ContainerStart(t.Context(), created.ID, container.StartOptions{}); e != nil {
			return e
		}
		if e = s.waitServiceStable(t.Context(), created.ID); e != nil {
			return e
		}
	}
	t.Log("Restored services started; configured health checks passed. Services without a health check were verified running only.")
	complete := *cp
	complete.Created = nil
	complete.RecoveryTask = ""
	if e = t.Checkpoint(complete); e != nil {
		return e
	}
	*cp = complete
	return nil
}

func (s *Service) waitServiceStable(ctx context.Context, id string) error {
	deadline := time.NewTimer(120 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	stable := 0
	for {
		ct, e := s.app.Docker.ContainerInspect(ctx, id)
		if e != nil {
			return e
		}
		if ct.State == nil || !ct.State.Running {
			return errors.New("restored service failed to remain running")
		}
		if ct.State.Health == nil {
			stable++
			if stable >= 3 {
				return nil
			}
		} else {
			if ct.State.Health.Status == "healthy" {
				return nil
			}
			if ct.State.Health.Status == "unhealthy" {
				return errors.New("restored service reported unhealthy")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("restored service health verification timed out")
		case <-tick.C:
		}
	}
}
