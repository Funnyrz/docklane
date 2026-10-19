package workerimage

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

// TestEnsureRealDocker is opt-in and only handles the explicitly owned fixture
// tags. It never builds, prunes or changes unrelated Docker images.
func TestEnsureRealDocker(t *testing.T) {
	host := os.Getenv("DM_TEST_WORKER_DOCKER_HOST")
	if host == "" {
		t.Skip("set DM_TEST_WORKER_DOCKER_HOST and fixture archive/version/architecture to exercise Docker loading")
	}
	archivePath := os.Getenv("DM_TEST_WORKER_ARCHIVE")
	version := os.Getenv("DM_TEST_WORKER_VERSION")
	arch := os.Getenv("DM_TEST_WORKER_ARCH")
	if version != "0.0.0-beta.90001" || (arch != "amd64" && arch != "arm64") || archivePath == "" {
		t.Fatal("real Docker test requires the owned 0.0.0-beta.90001 fixture archive and explicit amd64/arm64")
	}
	reference := "docklane:v" + version
	native := reference + "-" + arch
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	docker, err := client.NewClientWithOpts(client.WithHost(host), client.WithVersion("1.48"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	for _, tag := range []string{reference, native} {
		_, err := docker.ImageInspect(ctx, tag)
		if !errdefs.IsNotFound(err) {
			t.Fatalf("refusing test: owned fixture tag %q must be absent (inspect: %v)", tag, err)
		}
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := archive.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() > maxCompressed {
		t.Fatal("fixture archive exceeds limit")
	}
	zipped, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	members, err := scanArchive(ctx, zipped, func(name string) bool {
		return name == "manifest.json" || name == "index.json" || strings.HasPrefix(name, "blobs/sha256/") || strings.HasSuffix(name, ".json")
	})
	zipped.Close()
	archive.Close()
	if err != nil {
		t.Fatal(err)
	}
	var manifests []struct{ Config string }
	if err = json.Unmarshal(members["manifest.json"].data, &manifests); err != nil || len(manifests) != 1 {
		t.Fatal("fixture must contain one native image")
	}
	configID := "sha256:" + members[manifests[0].Config].digest
	expected := configID
	if index, present := members["index.json"]; present {
		var roots struct{ Manifests []struct{ Digest string } }
		if err = json.Unmarshal(index.data, &roots); err != nil || len(roots.Manifests) != 1 {
			t.Fatal("fixture must contain one native OCI image")
		}
		// A classic store identifies this image by its config. Importing that
		// same content into containerd can expose an OCI manifest/index ID.
		// Use the classic identity here to exercise real exported-content proof.
	}
	// Full prevalidation occurs before any Docker mutation, including cleanup.
	proof, err := verifyWorker(ctx, archivePath, t.TempDir()+"/fixture.tar", reference, arch, expected)
	if err != nil || proof.configID != configID {
		t.Fatalf("invalid owned fixture: %v", err)
	}
	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	asset := "docklane-images-" + arch + ".tar.gz"
	materials := makeArchive(map[string][]byte{"VERSION": []byte(version + "\n"), "IMAGE-SHA256SUMS": []byte(checksum(archiveData) + "  " + asset + "\n"), "IMAGES.txt": []byte("[" + native + "] " + expected + " linux/" + arch + "\n")})
	sums := []byte(checksum(archiveData) + "  " + asset + "\n" + checksum(materials) + "  docklane-installation.tar.gz\n")
	requests := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "RELEASE-SHA256SUMS":
			w.Write(sums)
		case "docklane-installation.tar.gz":
			w.Write(materials)
		case asset:
			w.Write(archiveData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	opts := options{arch: arch, version: version, http: server.Client(), bases: []string{server.URL + "/"}, tempDir: t.TempDir()}
	cleanupID := ""
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		for _, tag := range []string{reference, native} {
			found, inspectErr := docker.ImageInspect(cleanupCtx, tag)
			if errdefs.IsNotFound(inspectErr) {
				continue
			}
			if inspectErr != nil {
				t.Errorf("cleanup inspect %s: %v", tag, inspectErr)
				continue
			}
			if cleanupID == "" || found.ID != cleanupID {
				t.Errorf("refusing cleanup of changed/unverified fixture tag %s", tag)
				continue
			}
			if _, removeErr := docker.ImageRemove(cleanupCtx, tag, image.RemoveOptions{PruneChildren: false}); removeErr != nil {
				t.Errorf("cleanup tag %s: %v", tag, removeErr)
			}
		}
	}()
	if err = ensure(ctx, docker, reference, func(message string) { t.Log(message) }, opts); err != nil {
		t.Fatal(err)
	}
	loaded, err := docker.ImageInspect(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	cleanupID = loaded.ID
	if requests.Load() != 3 {
		t.Fatalf("expected three native release downloads, got %d", requests.Load())
	}
	if err = ensure(ctx, docker, reference, func(message string) { t.Log(message) }, opts); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatal("second worker use unexpectedly downloaded assets")
	}
	if err = validateImage(loaded, reference, arch); err != nil {
		t.Fatal(err)
	}
	t.Log(fmt.Sprintf("real Docker worker import and reuse verified: %s %s linux/%s", reference, loaded.ID, arch))
}
