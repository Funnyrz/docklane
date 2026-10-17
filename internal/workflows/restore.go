package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/safepath"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gin-gonic/gin"
)

func copyDockerOutput(out io.Writer, in io.Reader) (int64, error) {
	return stdcopy.StdCopy(out, io.Discard, in)
}
func checkFree(p string) error {
	var st syscall.Statfs_t
	if e := syscall.Statfs(p, &st); e != nil {
		return e
	}
	if uint64(st.Bavail)*uint64(st.Bsize) < 64<<20 {
		return errors.New("repository/restore filesystem needs at least 64 MiB available space; actual backup size may require more")
	}
	return nil
}

type restoreRequest struct {
	AcknowledgeUnverified bool              `json:"acknowledge_unverified"`
	RepositoryID          string            `json:"repository_id"`
	Snapshot              string            `json:"snapshot"`
	Source                string            `json:"source"`
	Target                string            `json:"target"`
	Overwrite             bool              `json:"overwrite"`
	Confirm               bool              `json:"confirm"`
	ConfirmOverwrite      string            `json:"confirm_overwrite"`
	Revision              string            `json:"revision"`
	Recreate              bool              `json:"recreate"`
	ContainerIDs          []string          `json:"container_ids"`
	ProjectIDs            []string          `json:"project_ids"`
	ContainerNames        map[string]string `json:"container_names"`
	ProjectNames          map[string]string `json:"project_names"`
	TargetState           string            `json:"-"`
}
type snapshotNode struct {
	StructType string `json:"struct_type"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
	Mode       uint32 `json:"mode"`
	Size       uint64 `json:"size"`
}

func (s *Service) readManifest(ctx context.Context, r Repository, ref string) (Manifest, []snapshotNode, error) {
	var manifest Manifest
	if !validDockerID(ref) {
		return manifest, nil, errors.New("an immutable full 64-character snapshot ID is required")
	}
	b, e := s.restic(ctx, r, "ls", "--json", ref)
	if e != nil {
		return manifest, nil, e
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	nodes := []snapshotNode{}
	manifestPath := ""
	for {
		var n snapshotNode
		e = dec.Decode(&n)
		if e == io.EOF {
			break
		}
		if e != nil {
			return manifest, nil, e
		}
		if n.StructType == "node" {
			nodes = append(nodes, n)
			if n.Name == "manifest.json" && (strings.Contains(n.Path, "/.dm-manifest-") || n.Path == "/manifest/manifest.json") {
				manifestPath = n.Path
			}
		}
	}
	if manifestPath == "" {
		return manifest, nil, errors.New("snapshot has no Docklane manifest; manual Restic recovery required")
	}
	b, e = s.restic(ctx, r, "dump", ref, manifestPath)
	if e != nil {
		return manifest, nil, e
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		return manifest, nil, e
	}
	if e = validateManifest(manifest); e != nil {
		return manifest, nil, e
	}
	return manifest, nodes, nil
}
func (s *Service) restorePreflight(ctx context.Context, req restoreRequest) (Repository, Manifest, []snapshotNode, string, error) {
	var m Manifest
	r, e := s.getRepository(req.RepositoryID)
	if e != nil {
		return r, m, nil, "", e
	}
	target, e := s.path(req.Target)
	if e != nil {
		return r, m, nil, "", e
	}
	repo, e := s.path(r.Location)
	if e != nil {
		return r, m, nil, "", e
	}
	if e = safepath.Disjoint(target, repo); e != nil {
		return r, m, nil, "", e
	}
	st, e := os.Stat(target)
	if e != nil && !os.IsNotExist(e) {
		return r, m, nil, "", e
	}
	if e == nil && !st.IsDir() {
		return r, m, nil, "", errors.New("restore target must be a directory")
	}
	if !req.Overwrite && e == nil {
		entries, e := os.ReadDir(target)
		if e != nil || len(entries) > 0 {
			return r, m, nil, "", errors.New("default restore requires a new or empty directory")
		}
	}
	parent := target
	for {
		if _, e = os.Stat(parent); e == nil {
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			return r, m, nil, "", errors.New("restore parent unavailable")
		}
		parent = next
	}
	if e = checkFree(parent); e != nil {
		return r, m, nil, "", e
	}
	m, nodes, e := s.readManifest(ctx, r, req.Snapshot)
	if e != nil {
		return r, m, nil, "", e
	}
	verification, _, e := s.snapshotVerification(ctx, r, req.Snapshot)
	if e != nil {
		return r, m, nil, "", e
	}
	if verification != "verified" {
		if !req.AcknowledgeUnverified || req.Overwrite || req.Recreate {
			return r, m, nil, "", errors.New(unverifiedSnapshotWarning + " Set acknowledge_unverified=true only for new-directory file rescue.")
		}
	}
	if req.Overwrite {
		src, selectionErr := s.overwriteSource(req, m)
		if selectionErr != nil {
			return r, m, nil, "", selectionErr
		}
		req.Source = src.SnapshotPath
		nodes = rebaseNodes(nodes, src.SnapshotPath)
		if len(nodes) == 0 {
			return r, m, nil, "", errors.New("selected overwrite source has no saved nodes")
		}
		if nodes[0].Type == "file" && nodes[0].Path == "/" {
			return r, m, nil, "", errors.New("file source overwrite requires manual recovery into its parent directory")
		}
	}
	var bytes uint64
	for _, n := range nodes {
		if n.Type == "file" {
			if ^uint64(0)-bytes < n.Size {
				return r, m, nil, "", errors.New("snapshot file sizes overflow supported range")
			}
			bytes += n.Size
		}
	}
	var fs syscall.Statfs_t
	if e = syscall.Statfs(parent, &fs); e != nil {
		return r, m, nil, "", e
	}
	if bytes > uint64(fs.Bavail)*uint64(fs.Bsize) {
		return r, m, nil, "", errors.New("insufficient free space for restored files")
	}
	if _, e = s.resourcePreflight(ctx, req, m); e != nil {
		return r, m, nil, "", e
	}
	verificationNodes := dataNodes(m, nodes)
	if req.Overwrite {
		verificationNodes = nodes
	}
	if needsRestoreWorker(verificationNodes) {
		if s.app.Docker == nil {
			return r, m, nil, "", errors.New("foreign ownership restoration requires the confined Docker restore worker")
		}
		if _, e = s.app.Docker.ImageInspect(ctx, s.app.Config.BackupWorkerImage); e != nil {
			return r, m, nil, "", fmt.Errorf("restore worker image unavailable; install the pinned worker image before restoring foreign UID/GID: %w", e)
		}
	}
	req.TargetState, e = restoreTargetState(target, nodes)
	if e != nil {
		return r, m, nil, "", e
	}
	fingerprint := restoreRevision(req, m, nodes)
	return r, m, nodes, fingerprint, nil
}
func restoreRevision(req restoreRequest, m Manifest, nodes []snapshotNode) string {
	req.Confirm = false
	req.ConfirmOverwrite = ""
	req.Revision = ""
	return hash([]byte(jsonString(struct {
		Request                             restoreRequest
		Repo, Snapshot, Target, TargetState string
		Overwrite                           bool
		Manifest                            Manifest
		Nodes                               []snapshotNode
	}{req, req.RepositoryID, req.Snapshot, req.Target, req.TargetState, req.Overwrite, m, nodes})))
}
func (s *Service) previewRestore(c *gin.Context) {
	var req restoreRequest
	if !bind(c, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	r, m, nodes, revision, e := s.restorePreflight(ctx, req)
	if e != nil {
		fail(c, e)
		return
	}
	resourcePlan, e := s.resourcePreflight(ctx, req, m)
	if e != nil {
		fail(c, e)
		return
	}
	for i := range resourcePlan.Projects {
		resourcePlan.Projects[i].Environment = nil
	}
	sensitive := m
	sensitive.ComposeConfig = nil
	for i := range sensitive.Containers {
		sensitive.Containers[i] = json.RawMessage(`{"detail":"container configuration retained in encrypted snapshot; exported only through authenticated recovery tooling"}`)
	}
	for i := range sensitive.Compose {
		sensitive.Compose[i].Environment = nil
	}
	warning := "New-directory recovery retains snapshot absolute hierarchy below the target. Restoring files alone does not verify an application."
	if req.Overwrite {
		warning = "Original-location recovery replaces the selected source contents directly at its exact authorized host path. Shared writers are stopped. A failed or interrupted restore may leave partially replaced data; writers stay stopped until you inspect the data and explicitly start them."
	}
	verification, verificationWarning, e := s.snapshotVerification(ctx, r, req.Snapshot)
	if e != nil {
		fail(c, e)
		return
	}
	if verification != "verified" {
		warning = verificationWarning + " " + warning
	}
	c.JSON(200, gin.H{"revision": revision, "manifest": sensitive, "files": len(nodes), "target": req.Target, "overwrite": req.Overwrite, "warning": warning, "verification_status": verification, "verification_warning": verificationWarning, "service_recreation_available": verification == "verified", "resources": resourcePlan})
}
func (s *Service) restore(c *gin.Context) {
	var req restoreRequest
	if !bind(c, &req) {
		return
	}
	if !req.Confirm || req.Overwrite && req.ConfirmOverwrite != req.Target {
		fail(c, errors.New("restore confirmation required; overwrite additionally requires confirm_overwrite equal to the full target path"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	_, _, _, revision, e := s.restorePreflight(ctx, req)
	cancel()
	if e != nil {
		fail(c, e)
		return
	}
	if req.Revision != revision {
		fail(c, errors.New("restore preview changed or missing; preview again"))
		return
	}
	platform.Submit(c, s.app, "restore", req.RepositoryID, false, func(t *task.Run) (err error) {
		if !s.lock("repo:" + req.RepositoryID) {
			return errors.New("repository busy")
		}
		defer s.unlock("repo:" + req.RepositoryID)
		r, manifest, nodes, rev, e := s.restorePreflight(t.Context(), req)
		if e != nil {
			return e
		}
		if rev != req.Revision {
			return errors.New("restore inputs changed after confirmation")
		}
		target, e := s.path(req.Target)
		if e != nil {
			return e
		}
		cp := stopCheckpoint{Version: 1}
		defer func() {
			if cp.HoldWriters && err != nil {
				err = &RestoreValidationError{Err: err}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if recoveryErr := s.recoverBackup(ctx, []byte(jsonString(cp))); recoveryErr != nil {
				err = &CompensationError{Err: errors.Join(err, recoveryErr)}
			}
		}()
		if req.Overwrite {
			cp.Running, e = s.writers(t.Context(), []backupSource{{HostPath: req.Target, LocalPath: target}})
			if e != nil {
				return e
			}
			if e = t.Checkpoint(cp); e != nil {
				return e
			}
			for _, id := range cp.Running {
				if e = s.app.Docker.ContainerStop(t.Context(), id, container.StopOptions{}); e != nil {
					return e
				}
			}
		}
		if e = os.MkdirAll(target, 0700); e != nil {
			return e
		}
		if req.Overwrite {
			cp.HoldWriters = true
			if e = t.Checkpoint(cp); e != nil {
				return e
			}
		}
		t.Progress("restore_and_verify_content", 20)
		restoreRef := req.Snapshot
		if req.Overwrite {
			src, e := s.overwriteSource(req, manifest)
			if e != nil {
				return e
			}
			restoreRef += ":" + src.SnapshotPath
		}
		verifyNodes := dataNodes(manifest, nodes)
		if req.Overwrite {
			verifyNodes = nodes
		}
		if needsRestoreWorker(verifyNodes) {
			if e = s.restoreInWorker(t, r, req, manifest, verifyNodes, target, &cp); e != nil {
				return e
			}
		} else {
			if _, e = s.restic(t.Context(), r, "restore", restoreRef, "--target", target, "--verify"); e != nil {
				return e
			}
			t.Progress("verify_metadata", 80)
			if req.Overwrite {
				for _, node := range nodes {
					if node.Path == "/" {
						if e = os.Chmod(target, os.FileMode(node.Mode&0777)); e != nil {
							return e
						}
						st, e := os.Stat(target)
						if e != nil {
							return e
						}
						if stat, ok := st.Sys().(*syscall.Stat_t); ok && (stat.Uid != node.UID || stat.Gid != node.GID) {
							if e = os.Chown(target, int(node.UID), int(node.GID)); e != nil {
								return e
							}
						}
					}
				}
			}
			if e = verifyRestored(target, verifyNodes); e != nil {
				return e
			}
		}

		if req.Overwrite {
			validated := cp
			validated.HoldWriters = false
			if e = t.Checkpoint(validated); e != nil {
				return e
			}
			cp = validated
			resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			resumeErr := s.recoverBackup(resumeCtx, []byte(jsonString(cp)))
			resumeCancel()
			if resumeErr != nil {
				return &CompensationError{Err: resumeErr}
			}
			cp = stopCheckpoint{Version: 1}
			if e = t.Checkpoint(cp); e != nil {
				return e
			}
		}
		t.Log("File contents verified by Restic and file modes/UID/GID checked.")
		if req.Recreate {
			t.Progress("recreate_and_verify_services", 90)
			return s.recreateResources(t, req, manifest, &cp)
		}
		t.Log("Service health was not evaluated because recreation was not requested.")
		return nil
	})
}
func verifyRestored(target string, nodes []snapshotNode) error {
	for _, n := range nodes {
		if n.Path == "" {
			continue
		}
		rel := strings.TrimPrefix(filepath.Clean(n.Path), "/")
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return errors.New("unsafe snapshot path")
		}
		q := filepath.Join(target, rel)
		parent := filepath.Dir(q)
		if _, e := safepath.Resolve(parent, []string{target}); q != target && e != nil {
			return fmt.Errorf("restored path escaped target: %w", e)
		}
		st, e := os.Lstat(q)
		if e != nil {
			return fmt.Errorf("restored node missing: %s", n.Path)
		}
		if n.Type != "symlink" {
			if uint32(st.Mode().Perm()) != n.Mode&0777 {
				return fmt.Errorf("mode verification failed: %s", n.Path)
			}
		}
		if stat, ok := st.Sys().(*syscall.Stat_t); ok && (stat.Uid != n.UID || stat.Gid != n.GID) {
			return fmt.Errorf("ownership verification failed: %s", n.Path)
		}
	}
	return nil
}

func dataNodes(m Manifest, nodes []snapshotNode) []snapshotNode {
	out := []snapshotNode{}
	for _, node := range nodes {
		for _, src := range m.Sources {
			root := filepath.Clean(src.SnapshotPath)
			if node.Path == root || strings.HasPrefix(node.Path, root+"/") {
				out = append(out, node)
				break
			}
		}
	}
	return out
}

func validateManifest(m Manifest) error {
	if m.Version != 1 {
		return errors.New("unsupported backup manifest version")
	}
	if len(m.Sources) == 0 {
		return errors.New("backup manifest has no sources")
	}
	for _, raw := range m.Containers {
		var ct container.InspectResponse
		if e := json.Unmarshal(raw, &ct); e != nil || ct.ContainerJSONBase == nil || ct.Config == nil || ct.HostConfig == nil {
			return errors.New("backup manifest contains incomplete container configuration")
		}
	}
	seen := map[string]bool{}
	for _, src := range m.Sources {
		p := src.SnapshotPath
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsRune(p, 0) {
			return errors.New("unsafe snapshot source path in manifest")
		}
		if seen[p] {
			return errors.New("duplicate snapshot source in manifest")
		}
		seen[p] = true
		if src.Volume == "" && (!filepath.IsAbs(src.HostPath) || filepath.Clean(src.HostPath) != src.HostPath || src.HostPath == "/") {
			return errors.New("unsafe original source path in manifest")
		}
	}
	return nil
}

func restoreTargetState(target string, nodes []snapshotNode) (string, error) {
	var state strings.Builder
	state.WriteString(target)
	paths := []string{target}
	for _, node := range nodes {
		if !filepath.IsAbs(node.Path) || filepath.Clean(node.Path) != node.Path {
			return "", errors.New("unsafe snapshot node path")
		}
		paths = append(paths, filepath.Join(target, strings.TrimPrefix(node.Path, "/")))
	}
	for _, node := range nodes {
		if e := checkRestoreAncestors(target, node); e != nil {
			return "", e
		}
	}
	for _, path := range paths {
		st, e := os.Lstat(path)
		if os.IsNotExist(e) {
			state.WriteString(path + ":missing")
			continue
		}
		if e != nil {
			return "", e
		}
		state.WriteString(path)
		state.WriteString(fmt.Sprint(st.Size(), st.Mode(), st.ModTime().UnixNano()))
		if stat, ok := st.Sys().(*syscall.Stat_t); ok {
			state.WriteString(fmt.Sprint(stat.Dev, stat.Ino, stat.Uid, stat.Gid))
		}
	}
	return hash([]byte(state.String())), nil
}

func (s *Service) overwriteSource(req restoreRequest, m Manifest) (backupSource, error) {
	var selectedSource backupSource
	found := false
	for _, src := range m.Sources {
		if req.Source != "" && src.SnapshotPath != req.Source {
			continue
		}
		if req.Source == "" && len(m.Sources) != 1 {
			return selectedSource, errors.New("multiple sources: select the source snapshot_path for original-location overwrite")
		}
		selectedSource = src
		found = true
		break
	}
	if !found {
		return selectedSource, errors.New("selected overwrite source is absent from manifest")
	}
	original, e := s.path(selectedSource.HostPath)
	if e != nil {
		return selectedSource, fmt.Errorf("original source path is not authorized for overwrite: %w", e)
	}
	target, e := s.path(req.Target)
	if e != nil {
		return selectedSource, e
	}
	if original != target {
		return selectedSource, errors.New("original-location overwrite target must exactly equal the selected source's authorized original host path")
	}
	return selectedSource, nil
}
func rebaseNodes(nodes []snapshotNode, root string) []snapshotNode {
	out := []snapshotNode{}
	for _, node := range nodes {
		if node.Path == root {
			node.Path = "/"
			out = append(out, node)
		} else if strings.HasPrefix(node.Path, root+"/") {
			node.Path = strings.TrimPrefix(node.Path, root)
			out = append(out, node)
		}
	}
	return out
}

func checkRestoreAncestors(root string, node snapshotNode) error {
	q := filepath.Join(root, strings.TrimPrefix(node.Path, "/"))
	leaf := q
	for {
		st, e := os.Lstat(q)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil && st.Mode()&os.ModeSymlink != 0 && (q != leaf || node.Type != "symlink") {
			return errors.New("restore target contains a symlink where a directory or regular file will be restored")
		}
		if q == root {
			return nil
		}
		next := filepath.Dir(q)
		if next == q {
			return errors.New("snapshot path escaped restore root")
		}
		q = next
	}
}

type RestoreValidationError struct{ Err error }

func (e *RestoreValidationError) Error() string {
	return "original-location restore may contain partial data; original writers remain stopped. Inspect data and explicitly start services only after validation: " + e.Err.Error()
}
func (e *RestoreValidationError) Unwrap() error     { return e.Err }
func (e *RestoreValidationError) ErrorCode() string { return "RESTORE_VALIDATION_REQUIRED" }
