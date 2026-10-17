package workflows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/docker-manager/docker-manager/internal/restoreworker"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/errdefs"
)

func needsRestoreWorker(nodes []snapshotNode) bool {
	if os.Geteuid() == 0 {
		return false
	}
	for _, node := range nodes {
		if node.UID != uint32(os.Geteuid()) || node.GID != uint32(os.Getegid()) {
			return true
		}
		if node.Type == "file" && node.Mode&0400 == 0 || node.Type == "dir" && node.Mode&0100 == 0 {
			return true
		}
	}
	return false
}
func (s *Service) restoreInWorker(t *task.Run, r Repository, req restoreRequest, m Manifest, nodes []snapshotNode, target string, cp *stopCheckpoint) (err error) {
	spec := restoreworker.Spec{Snapshot: req.Snapshot, Nodes: []restoreworker.Node{}}
	if req.Overwrite {
		source, e := s.overwriteSource(req, m)
		if e != nil {
			return e
		}
		spec.Source = source.SnapshotPath
	}
	for _, node := range nodes {
		spec.Nodes = append(spec.Nodes, restoreworker.Node{Path: node.Path, Type: node.Type, UID: node.UID, GID: node.GID, Mode: node.Mode, Size: node.Size})
	}
	if e := spec.Validate(); e != nil {
		return e
	}
	repository, e := s.path(r.Location)
	if e != nil {
		return e
	}
	stage, e := os.MkdirTemp(repository, ".dm-manifest-")
	if e != nil {
		return e
	}
	cp.Stage, e = s.hostPath(stage)
	if e != nil {
		return e
	}
	if e = t.Checkpoint(cp); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(stage, "spec.json"), []byte(jsonString(spec)), 0600); e != nil {
		return e
	}
	targetHost, e := s.hostPath(target)
	if e != nil {
		return e
	}
	stageHost, e := s.hostPath(stage)
	if e != nil {
		return e
	}
	locks := filepath.Join(repository, "locks")
	if e = os.MkdirAll(locks, 0700); e != nil {
		return e
	}
	locksHost, e := s.hostPath(locks)
	if e != nil {
		return e
	}
	secret, e := s.app.Vault.Open(r.secret)
	if e != nil {
		return e
	}
	st, e := os.Stat(repository)
	if e != nil {
		return e
	}
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if stat, ok := st.Sys().(*syscall.Stat_t); ok {
		uid = stat.Uid
		gid = stat.Gid
	}
	cfg := &container.Config{Image: s.app.Config.BackupWorkerImage, User: "0:0", Entrypoint: []string{"/usr/local/bin/docklane", "restore-worker", "/spec/spec.json"}, Env: []string{"RESTIC_REPOSITORY=/repository", "RESTIC_PASSWORD=" + secret, fmt.Sprintf("DM_WORKER_UID=%d", uid), fmt.Sprintf("DM_WORKER_GID=%d", gid)}, Labels: map[string]string{"docker-manager.worker": "restore", "docker-manager.task": t.ID}}
	caps := []string{"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID"}
	if uid != 0 {
		caps = append(caps, "SETUID", "SETGID", "SETPCAP")
	}
	hc := &container.HostConfig{ReadonlyRootfs: true, NetworkMode: "none", CapDrop: []string{"ALL"}, CapAdd: caps, SecurityOpt: []string{"no-new-privileges:true"}, Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,mode=1777,size=128m"}, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: r.Location, Target: "/repository", ReadOnly: true}, {Type: mount.TypeBind, Source: locksHost, Target: "/repository/locks"}, {Type: mount.TypeBind, Source: targetHost, Target: "/target"}, {Type: mount.TypeBind, Source: stageHost, Target: "/spec", ReadOnly: true}}}
	created, e := s.app.Docker.ContainerCreate(t.Context(), cfg, hc, nil, nil, "")
	if e != nil {
		return fmt.Errorf("restore worker image or capabilities unavailable: %w", e)
	}
	cp.Worker = created.ID
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		e := s.app.Docker.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		if e != nil && !errdefs.IsNotFound(e) {
			err = &CompensationError{Err: errors.Join(err, e)}
			return
		}
		cp.Worker = ""
		if e = t.Checkpoint(cp); e != nil {
			err = errors.Join(err, e)
		}
	}()
	if e = t.Checkpoint(cp); e != nil {
		return e
	}
	if e = s.app.Docker.ContainerStart(t.Context(), created.ID, container.StartOptions{}); e != nil {
		return e
	}
	done, errs := s.app.Docker.ContainerWait(t.Context(), created.ID, container.WaitConditionNotRunning)
	select {
	case result := <-done:
		if result.StatusCode != 0 {
			return s.workerFailure(t, created.ID, fmt.Sprintf("restore worker failed (status %d); original data may be partially restored, inspect the task before restarting applications", result.StatusCode), secret)
		}
	case e := <-errs:
		if e != nil {
			return e
		}
		return errors.New("restore worker returned no exit status")
	case <-t.Context().Done():
		return t.Context().Err()
	}
	t.Log("Confined restore worker verified file contents, types, permissions and original UID/GID.")
	return nil
}
