// Package workerimage prepares release-matched backup workers only when needed.
package workerimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/docker-manager/docker-manager/internal/buildinfo"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

const maxCompressed = int64(512 << 20)

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-beta\.[1-9][0-9]*)?$`)
var canonicalReference = regexp.MustCompile(`^docklane:v[0-9]+\.[0-9]+\.[0-9]+(?:-beta\.[1-9][0-9]*)?$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// The token lock is held through validation and loading. Waiting callers can
// cancel independently; failures release the token so a later task can retry.
var locks = struct {
	sync.Mutex
	entries map[string]*endpointLock
}{entries: make(map[string]*endpointLock)}

type endpointLock struct {
	token chan struct{}
	users int
}

func acquire(ctx context.Context, key string) (func(), error) {
	locks.Lock()
	entry := locks.entries[key]
	if entry == nil {
		entry = &endpointLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		locks.entries[key] = entry
	}
	entry.users++
	locks.Unlock()
	drop := func() {
		locks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(locks.entries, key)
		}
		locks.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-entry.token:
		return func() { entry.token <- struct{}{}; drop() }, nil
	}
}

type options struct {
	arch, version string
	http          *http.Client
	bases         []string
	tempDir       string
}

func defaults() options {
	version := buildinfo.Version
	base := "https://github.com/Funnyrz/docklane/releases/download/v" + version + "/"
	return options{arch: runtime.GOARCH, version: version, http: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: secureRedirect}, bases: []string{"https://gh-proxy.com/" + base, "https://ghfast.top/" + base, base}}
}

// Ensure reuses a valid local image. Only the canonical reference for this
// application version is fetched automatically; custom images must exist locally.
// Downloads are pinned to the same release and verified before Docker sees them.
func Ensure(ctx context.Context, docker *client.Client, reference string, log func(string)) error {
	return ensure(ctx, docker, reference, log, defaults())
}
func ensure(ctx context.Context, docker *client.Client, reference string, log func(string), opts options) error {
	if docker == nil {
		return errors.New("backup worker requires a Docker client")
	}
	if log == nil {
		log = func(string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	unlock, err := acquire(ctx, docker.DaemonHost()+"\x00"+reference)
	if err != nil {
		return err
	}
	defer unlock()
	existing, err := docker.ImageInspect(ctx, reference)
	if err == nil {
		return validateImage(existing, reference, opts.arch)
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect backup worker: %w", err)
	}
	if reference != "docklane:v"+opts.version || !releaseVersion.MatchString(opts.version) {
		return fmt.Errorf("backup worker %q is not installed locally; import the configured image manually (automatic download only supports docklane:v%s)", reference, opts.version)
	}
	if opts.arch != "amd64" && opts.arch != "arm64" {
		return fmt.Errorf("unsupported backup worker architecture %q", opts.arch)
	}
	native := reference + "-" + opts.arch
	local, nativeErr := docker.ImageInspect(ctx, native)
	if nativeErr == nil {
		if !digestPattern.MatchString(local.ID) {
			return errors.New("invalid local worker identity")
		}
		if err = validateImage(local, reference, opts.arch); err != nil {
			return err
		}
		if err = ensureAlias(ctx, docker, local.ID, reference, opts.arch); err != nil {
			return err
		}
		log("Backup worker ready; reused the local native image without downloading")
		return nil
	}
	if !errdefs.IsNotFound(nativeErr) {
		return fmt.Errorf("inspect native backup worker: %w", nativeErr)
	}
	log("Preparing backup worker for linux/" + opts.arch + " (first use; release v" + opts.version + ")")
	work, err := os.MkdirTemp(opts.tempDir, "docklane-worker-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	manifestPath := filepath.Join(work, "RELEASE-SHA256SUMS")
	if err = download(ctx, opts, "RELEASE-SHA256SUMS", manifestPath, 64<<10, log); err != nil {
		return err
	}
	sumsData, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	sums, err := parseSums(sumsData)
	if err != nil {
		return fmt.Errorf("release checksum manifest: %w", err)
	}
	asset := "docklane-images-" + opts.arch + ".tar.gz"
	for _, name := range []string{"docklane-installation.tar.gz", asset} {
		if _, ok := sums[name]; !ok {
			return fmt.Errorf("release v%s lacks verified %s; update to a release with native worker assets or import the matching worker offline", opts.version, name)
		}
	}
	materialsPath := filepath.Join(work, "docklane-installation.tar.gz")
	if err = downloadVerified(ctx, opts, "docklane-installation.tar.gz", materialsPath, sums["docklane-installation.tar.gz"], 32<<20, log); err != nil {
		return err
	}
	expected, err := installationMetadata(ctx, materialsPath, opts.version, opts.arch, asset, sums[asset])
	if err != nil {
		return err
	}
	compressed := filepath.Join(work, asset)
	if err = downloadVerified(ctx, opts, asset, compressed, sums[asset], maxCompressed, log); err != nil {
		return err
	}
	log("Verifying backup worker archive, platform and application version")
	unpacked := filepath.Join(work, "worker.tar")
	verified, err := verifyWorker(ctx, compressed, unpacked, reference, opts.arch, expected)
	if err != nil {
		return err
	}
	for _, tag := range []string{native, reference} {
		found, inspectErr := docker.ImageInspect(ctx, tag)
		if inspectErr == nil {
			if found.ID != verified.configID && found.ID != expected {
				return fmt.Errorf("refusing to replace unrelated local worker tag %q", tag)
			}
			if err = validateImage(found, reference, opts.arch); err != nil {
				return err
			}
		} else if !errdefs.IsNotFound(inspectErr) {
			return inspectErr
		}
	}
	log("Loading verified backup worker for linux/" + opts.arch)
	input, err := os.Open(unpacked)
	if err != nil {
		return err
	}
	defer input.Close()
	response, err := docker.ImageLoad(ctx, input, client.ImageLoadWithQuiet(true))
	if err != nil {
		return fmt.Errorf("load backup worker: %w", err)
	}
	err = consumeLoadResponse(response.Body)
	response.Body.Close()
	if err != nil {
		return err
	}
	loaded, err := docker.ImageInspect(ctx, native)
	if err != nil {
		return err
	}
	if !digestPattern.MatchString(loaded.ID) {
		return errors.New("invalid loaded worker identity")
	}
	if loaded.ID != verified.configID && loaded.ID != expected {
		// Classic and containerd image stores expose different IDs for the same
		// content. Prove the exported config and layers instead of guessing IDs.
		exported, exportErr := docker.ImageSave(ctx, []string{native})
		if exportErr != nil {
			return fmt.Errorf("verify loaded worker contents: %w", exportErr)
		}
		exportedProof, proofErr := verifyTar(ctx, io.LimitReader(contextReader{ctx, exported}, maxExpanded+1), reference, opts.arch, verified.configID)
		exported.Close()
		if proofErr != nil {
			return fmt.Errorf("loaded worker content differs from verified release: %w", proofErr)
		}
		if exportedProof.configID != verified.configID {
			return errors.New("loaded worker content differs from verified release")
		}
	}
	if err = validateImage(loaded, reference, opts.arch); err != nil {
		return err
	}
	if err = ensureAlias(ctx, docker, loaded.ID, reference, opts.arch); err != nil {
		return err
	}
	log("Backup worker ready; subsequent tasks reuse the local image")
	return nil
}

func ensureAlias(ctx context.Context, docker *client.Client, identity, reference, arch string) error {
	// A user may have created the canonical tag while the native image was
	// inspected or loaded. Never replace a different concurrently created tag.
	canonical, inspectErr := docker.ImageInspect(ctx, reference)
	if inspectErr == nil && canonical.ID != identity {
		return fmt.Errorf("refusing to replace concurrently created worker tag %q", reference)
	}
	if inspectErr != nil && !errdefs.IsNotFound(inspectErr) {
		return inspectErr
	}
	if err := docker.ImageTag(ctx, identity, reference); err != nil {
		return err
	}
	final, err := docker.ImageInspect(ctx, reference)
	if err != nil {
		return err
	}
	if final.ID != identity {
		return errors.New("backup worker tag changed during preparation")
	}
	return validateImage(final, reference, arch)
}

func validateImage(found image.InspectResponse, reference, arch string) error {
	if found.Os != "linux" || found.Architecture != arch {
		return fmt.Errorf("backup worker platform mismatch: got %s/%s, need linux/%s", found.Os, found.Architecture, arch)
	}
	if canonicalReference.MatchString(reference) {
		if found.Config == nil {
			return errors.New("backup worker application version is missing")
		}
		values := []string{}
		for _, env := range found.Config.Env {
			if strings.HasPrefix(env, "DM_BACKUP_WORKER_IMAGE=") {
				values = append(values, env)
			}
		}
		if len(values) != 1 || values[0] != "DM_BACKUP_WORKER_IMAGE="+reference {
			return errors.New("backup worker application version mismatch")
		}
	}
	return nil
}
func consumeLoadResponse(body io.Reader) error {
	bounded := &io.LimitedReader{R: body, N: 4<<20 + 1}
	decoder := json.NewDecoder(bounded)
	for {
		var message struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		err := decoder.Decode(&message)
		if err == io.EOF {
			if bounded.N == 0 {
				return errors.New("worker load response exceeds size limit")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid worker load response: %w", err)
		}
		if message.Error != "" {
			return fmt.Errorf("worker load failed: %s", message.Error)
		}
		if message.ErrorDetail.Message != "" {
			return fmt.Errorf("worker load failed: %s", message.ErrorDetail.Message)
		}
	}
}
