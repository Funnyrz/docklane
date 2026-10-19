package workerimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

const testVersion = "0.3.2-beta.1"
const testRef = "docklane:v" + testVersion

func checksum(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
func makeArchive(entries map[string][]byte) []byte {
	var buffer bytes.Buffer
	zip := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(zip)
	for name, data := range entries {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			panic(err)
		}
		if _, err := writer.Write(data); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	if err := zip.Close(); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

type fixture struct {
	assets        map[string][]byte
	configID      string
	config        []byte
	workerEntries map[string][]byte
}

func newFixture() *fixture {
	layer := []byte("opaque Docker layer tar contents")
	config := mustJSON(map[string]any{"architecture": "amd64", "os": "linux", "config": map[string]any{"Env": []string{"DM_BACKUP_WORKER_IMAGE=" + testRef}}, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + checksum(layer)}}})
	configID := "sha256:" + checksum(config)
	configPath := "blobs/sha256/" + checksum(config)
	layerPath := "blobs/sha256/" + checksum(layer)
	entries := map[string][]byte{configPath: config, layerPath: layer, "manifest.json": mustJSON([]any{map[string]any{"Config": configPath, "Layers": []string{layerPath}, "RepoTags": []string{testRef + "-amd64"}}})}
	f := &fixture{assets: map[string][]byte{}, configID: configID, config: config, workerEntries: entries}
	f.refresh()
	return f
}
func (f *fixture) refresh() {
	worker := makeArchive(f.workerEntries)
	f.assets["docklane-images-amd64.tar.gz"] = worker
	materials := makeArchive(map[string][]byte{"VERSION": []byte(testVersion + "\n"), "IMAGE-SHA256SUMS": []byte(checksum(worker) + "  docklane-images-amd64.tar.gz\n"), "IMAGES.txt": []byte("[" + testRef + "-amd64] " + f.configID + " linux/amd64\n")})
	f.assets["docklane-installation.tar.gz"] = materials
	f.refreshSums()
}
func (f *fixture) refreshSums() {
	var sums strings.Builder
	for _, name := range []string{"docklane-images-amd64.tar.gz", "docklane-installation.tar.gz"} {
		sums.WriteString(checksum(f.assets[name]) + "  " + name + "\n")
	}
	f.assets["RELEASE-SHA256SUMS"] = []byte(sums.String())
}

type fakeDocker struct {
	sync.Mutex
	images        map[string]map[string]any
	loads         int
	tags          int
	inspectStatus int
	configID      string
	loadError     string
	exported      []byte
}

func (d *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	d.Lock()
	defer d.Unlock()
	name := r.URL.Path
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(name, "/images/get") {
		w.Write(d.exported)
		return
	}
	if strings.Contains(name, "/images/load") {
		data, err := io.ReadAll(r.Body)
		if err != nil || len(data) == 0 {
			http.Error(w, "missing load body", 400)
			return
		}
		d.loads++
		if d.loadError != "" {
			fmt.Fprintf(w, "{\"error\":%q}\n", d.loadError)
			return
		}
		d.images[testRef+"-amd64"] = imageDetails(d.configID, "amd64", testRef)
		io.WriteString(w, "{\"stream\":\"loaded\"}\n")
		return
	}
	if strings.HasSuffix(name, "/tag") {
		d.tags++
		target := strings.TrimPrefix(r.URL.Query().Get("repo"), "docker.io/library/") + ":" + r.URL.Query().Get("tag")
		d.images[target] = imageDetails(d.configID, "amd64", testRef)
		w.WriteHeader(201)
		return
	}
	if strings.HasSuffix(name, "/json") && strings.Contains(name, "/images/") {
		if d.inspectStatus != 0 {
			w.WriteHeader(d.inspectStatus)
			io.WriteString(w, `{"message":"daemon error"}`)
			return
		}
		reference := strings.TrimSuffix(strings.SplitN(name, "/images/", 2)[1], "/json")
		image, ok := d.images[reference]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"image missing"}`)
			return
		}
		json.NewEncoder(w).Encode(image)
		return
	}
	http.Error(w, "unrecognized fake Docker route "+name, 400)
}
func imageDetails(id, arch, ref string) map[string]any {
	return map[string]any{"Id": id, "Os": "linux", "Architecture": arch, "Config": map[string]any{"Env": []string{"DM_BACKUP_WORKER_IMAGE=" + ref}}}
}
func dockerClient(t *testing.T, d *fakeDocker) *client.Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(s.Close)
	c, err := client.NewClientWithOpts(client.WithHost(s.URL), client.WithVersion("1.48"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func optionsFor(t *testing.T, f *fixture) (options, *atomic.Int32) {
	t.Helper()
	requests := new(atomic.Int32)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		data, ok := f.assets[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(s.Close)
	return options{arch: "amd64", version: testVersion, http: s.Client(), bases: []string{s.URL + "/"}, tempDir: t.TempDir()}, requests
}
func TestEnsureReusesLocalWithoutNetwork(t *testing.T) {
	for _, test := range []struct {
		name, reference, arch, env string
		wantErr                    bool
	}{
		{"canonical", testRef, "amd64", testRef, false},
		{"custom", "private/worker:custom", "amd64", "other", false},
		{"wrong architecture", testRef, "arm64", testRef, true},
		{"wrong version", testRef, "amd64", "docklane:v0.1.0", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &fakeDocker{images: map[string]map[string]any{test.reference: imageDetails("sha256:"+strings.Repeat("1", 64), test.arch, test.env)}}
			f := newFixture()
			opts, count := optionsFor(t, f)
			err := ensure(context.Background(), dockerClient(t, d), test.reference, nil, opts)
			if (err != nil) != test.wantErr {
				t.Fatalf("error %v", err)
			}
			if count.Load() != 0 {
				t.Fatal("reused image attempted network download")
			}
		})
	}
}
func TestEnsureFetchesOnlyNativeWorkerAndCleansUp(t *testing.T) {
	f := newFixture()
	opts, count := optionsFor(t, f)
	d := &fakeDocker{images: map[string]map[string]any{}, configID: f.configID}
	var logs []string
	err := ensure(context.Background(), dockerClient(t, d), testRef, func(s string) { logs = append(logs, s) }, opts)
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != 3 || d.loads != 1 || d.tags != 1 {
		t.Fatalf("downloads %d loads %d tags %d", count.Load(), d.loads, d.tags)
	}
	entries, err := os.ReadDir(opts.tempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary files remain: %v %v", entries, err)
	}
	if len(logs) < 6 {
		t.Fatal("missing progress messages")
	}
}
func TestEnsureMissingCustomAndDaemonFailureNeverDownload(t *testing.T) {
	for _, test := range []struct {
		ref    string
		status int
	}{{"custom:missing", 0}, {"docklane:v0.1.0", 0}, {testRef, 500}, {testRef, 403}} {
		f := newFixture()
		opts, count := optionsFor(t, f)
		d := &fakeDocker{images: map[string]map[string]any{}, inspectStatus: test.status}
		if err := ensure(context.Background(), dockerClient(t, d), test.ref, nil, opts); err == nil {
			t.Fatal("expected failure")
		}
		if count.Load() != 0 {
			t.Fatal("unexpected network")
		}
	}
}
func TestEnsureConcurrentFirstUseDownloadsOnce(t *testing.T) {
	f := newFixture()
	opts, count := optionsFor(t, f)
	d := &fakeDocker{images: map[string]map[string]any{}, configID: f.configID}
	c := dockerClient(t, d)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- ensure(context.Background(), c, testRef, nil, opts) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 3 || d.loads != 1 {
		t.Fatalf("download count %d load count %d", count.Load(), d.loads)
	}
}
func TestEnsureRejectsBrokenReleaseBeforeLoad(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fixture)
	}{
		{"missing native asset", func(f *fixture) {
			f.assets["RELEASE-SHA256SUMS"] = []byte(checksum(f.assets["docklane-installation.tar.gz"]) + "  docklane-installation.tar.gz\n")
		}},
		{"checksum mismatch", func(f *fixture) { f.assets["docklane-images-amd64.tar.gz"] = []byte("wrong") }},
		{"unsafe checksum path", func(f *fixture) {
			f.assets["RELEASE-SHA256SUMS"] = []byte(strings.Repeat("1", 64) + "  ../image.tar.gz\n")
		}},
		{"unsafe archive path", func(f *fixture) { f.workerEntries["../escape"] = []byte("unsafe"); f.refresh() }},
		{"identity mismatch", func(f *fixture) { f.configID = "sha256:" + strings.Repeat("1", 64); f.refresh() }},
		{"wrong platform", func(f *fixture) { f.changeConfig("arm64", testRef) }},
		{"wrong app version", func(f *fixture) { f.changeConfig("amd64", "docklane:v0.1.0") }},
		{"wrong tag", func(f *fixture) {
			f.workerEntries["manifest.json"] = mustJSON([]any{map[string]any{"Config": "none", "Layers": []string{}, "RepoTags": []string{testRef + "-arm64"}}})
			f.refresh()
		}},
		{"truncated gzip", func(f *fixture) {
			asset := f.assets["docklane-images-amd64.tar.gz"]
			f.assets["docklane-images-amd64.tar.gz"] = asset[:len(asset)-10]
			f.materialWithCurrentWorker()
		}},
		{"unsafe installation", func(f *fixture) {
			f.assets["docklane-installation.tar.gz"] = makeArchive(map[string][]byte{"../escape": []byte("bad")})
			f.refreshSums()
		}},
		{"inner checksum mismatch", func(f *fixture) {
			f.assets["docklane-installation.tar.gz"] = makeArchive(map[string][]byte{"VERSION": []byte(testVersion), "IMAGE-SHA256SUMS": []byte(strings.Repeat("0", 64) + "  docklane-images-amd64.tar.gz\n")})
			f.refreshSums()
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			test.mutate(f)
			opts, _ := optionsFor(t, f)
			d := &fakeDocker{images: map[string]map[string]any{}, configID: f.configID}
			if err := ensure(context.Background(), dockerClient(t, d), testRef, nil, opts); err == nil {
				t.Fatal("expected validation rejection")
			}
			if d.loads != 0 || d.tags != 0 {
				t.Fatal("unverified image reached Docker")
			}
		})
	}
}
func (f *fixture) changeConfig(arch, ref string) {
	var config map[string]any
	json.Unmarshal(f.config, &config)
	config["architecture"] = arch
	config["config"] = map[string]any{"Env": []string{"DM_BACKUP_WORKER_IMAGE=" + ref}}
	for key := range f.workerEntries {
		if strings.Contains(key, strings.TrimPrefix(f.configID, "sha256:")) {
			delete(f.workerEntries, key)
		}
	}
	f.config = mustJSON(config)
	f.configID = "sha256:" + checksum(f.config)
	p := "blobs/sha256/" + checksum(f.config)
	f.workerEntries[p] = f.config
	var manifest []map[string]any
	json.Unmarshal(f.workerEntries["manifest.json"], &manifest)
	manifest[0]["Config"] = p
	f.workerEntries["manifest.json"] = mustJSON(manifest)
	f.refresh()
}
func (f *fixture) materialWithCurrentWorker() {
	f.assets["docklane-installation.tar.gz"] = makeArchive(map[string][]byte{"VERSION": []byte(testVersion), "IMAGE-SHA256SUMS": []byte(checksum(f.assets["docklane-images-amd64.tar.gz"]) + "  docklane-images-amd64.tar.gz\n"), "IMAGES.txt": []byte("[" + testRef + "-amd64] " + f.configID + " linux/amd64\n")})
	f.refreshSums()
}
func TestEnsureRejectsInvalidNativeIdentity(t *testing.T) {
	f := newFixture()
	opts, _ := optionsFor(t, f)
	d := &fakeDocker{images: map[string]map[string]any{testRef + "-amd64": imageDetails("invalid-identity", "amd64", testRef)}, configID: f.configID}
	if err := ensure(context.Background(), dockerClient(t, d), testRef, nil, opts); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("error %v", err)
	}
	if d.loads != 0 {
		t.Fatal("unrelated tag overwritten")
	}
}
func TestEnsureLoadErrorsAreNotSuccessAndCanRetry(t *testing.T) {
	f := newFixture()
	opts, _ := optionsFor(t, f)
	d := &fakeDocker{images: map[string]map[string]any{}, configID: f.configID, loadError: "disk full"}
	c := dockerClient(t, d)
	if err := ensure(context.Background(), c, testRef, nil, opts); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error %v", err)
	}
	d.Lock()
	d.loadError = ""
	d.Unlock()
	if err := ensure(context.Background(), c, testRef, nil, opts); err != nil {
		t.Fatal(err)
	}
}
func TestCancelledWaitDoesNotDisturbActiveDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unlock, err := acquire(context.Background(), "test-lock")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := acquire(ctx, "test-lock"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait ignored cancellation")
	}
	unlock()
	release, err := acquire(context.Background(), "test-lock")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestDownloadCancellationAndFallback(t *testing.T) {
	f := newFixture()
	opts, _ := optionsFor(t, f)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) }))
	defer first.Close()
	opts.bases = append([]string{first.URL + "/"}, opts.bases...)
	target := filepath.Join(t.TempDir(), "manifest")
	if err := download(context.Background(), opts, "RELEASE-SHA256SUMS", target, 64<<10, func(string) {}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := download(ctx, opts, "RELEASE-SHA256SUMS", target, 64<<10, func(string) {}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestArchiveRejectsSpecialLinksAndBombs(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "symlink", Typeflag: tar.TypeSymlink, Linkname: "/etc"}, {Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "file"}, {Name: "huge", Typeflag: tar.TypeReg, Size: maxExpanded + 1}, {Name: "/absolute", Typeflag: tar.TypeReg}} {
		var data bytes.Buffer
		writer := tar.NewWriter(&data)
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := scanArchive(context.Background(), bytes.NewReader(data.Bytes()), func(string) bool { return true }); err == nil {
			t.Fatalf("accepted %#v", header)
		}
	}
}

func TestEnsureAcceptsContainerdManifestIdentity(t *testing.T) {
	f := newFixture()
	descriptor := mustJSON(map[string]any{"schemaVersion": 2, "config": map[string]any{"digest": f.configID}})
	identity := "sha256:" + checksum(descriptor)
	f.workerEntries["blobs/sha256/"+checksum(descriptor)] = descriptor
	f.workerEntries["index.json"] = mustJSON(map[string]any{"schemaVersion": 2, "manifests": []any{map[string]any{"digest": identity, "annotations": map[string]string{"io.containerd.image.name": "docker.io/library/" + testRef + "-amd64", "org.opencontainers.image.ref.name": "v" + testVersion + "-amd64"}}}})
	configID := f.configID
	f.configID = identity
	f.refresh()
	opts, _ := optionsFor(t, f)
	d := &fakeDocker{images: map[string]map[string]any{}, configID: configID}
	if err := ensure(context.Background(), dockerClient(t, d), testRef, nil, opts); err != nil {
		t.Fatal(err)
	}
}
func TestEnsureRejectsExtraOCIIndexTag(t *testing.T) {
	f := newFixture()
	f.workerEntries["index.json"] = mustJSON(map[string]any{"manifests": []any{map[string]any{"digest": f.configID}, map[string]any{"digest": f.configID}}})
	f.refresh()
	opts, _ := optionsFor(t, f)
	d := &fakeDocker{images: map[string]map[string]any{}, configID: f.configID}
	if err := ensure(context.Background(), dockerClient(t, d), testRef, nil, opts); err == nil {
		t.Fatal("extra OCI image admitted")
	}
	if d.loads != 0 {
		t.Fatal("unsafe load")
	}
}
func TestDownloadInterruptedResponseAndSizeLimit(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "500")
		w.Write([]byte("short"))
	}))
	defer s.Close()
	if err := fetch(context.Background(), s.Client(), s.URL, filepath.Join(t.TempDir(), "short"), 1000, func(int64, int64) {}); err == nil {
		t.Fatal("truncated response accepted")
	}
	if err := fetch(context.Background(), s.Client(), s.URL, filepath.Join(t.TempDir(), "large"), 100, func(int64, int64) {}); err == nil {
		t.Fatal("oversized response accepted")
	}
}
func TestEnsureCancellationWhileDownloadingIsRetryable(t *testing.T) {
	f := newFixture()
	ready := make(chan struct{})
	var once sync.Once
	var abort atomic.Bool
	abort.Store(true)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if abort.Load() {
			once.Do(func() { close(ready) })
			<-r.Context().Done()
			return
		}
		w.Write(f.assets[strings.TrimPrefix(r.URL.Path, "/")])
	}))
	defer s.Close()
	opts := options{arch: "amd64", version: testVersion, http: s.Client(), bases: []string{s.URL + "/"}, tempDir: t.TempDir()}
	d := &fakeDocker{images: map[string]map[string]any{}, configID: f.configID}
	c := dockerClient(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ensure(ctx, c, testRef, nil, opts) }()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("download ignored cancellation")
	}
	remaining, readErr := os.ReadDir(opts.tempDir)
	if readErr != nil || len(remaining) != 0 {
		t.Fatalf("cancelled download left temporary files: %v %v", remaining, readErr)
	}
	abort.Store(false)
	if err := ensure(context.Background(), c, testRef, nil, opts); err != nil {
		t.Fatal(err)
	}
}
func TestChecksumManifestRejectsDuplicatesAndMalformedEntries(t *testing.T) {
	line := strings.Repeat("1", 64) + "  worker.tar.gz\n"
	for _, data := range []string{"", line + line, line + "\n", strings.Repeat("1", 64) + "  /absolute\n", strings.Repeat("1", 64) + "  a/b\n"} {
		if _, err := parseSums([]byte(data)); err == nil {
			t.Fatalf("accepted manifest %q", data)
		}
	}
}

func TestRejectTLSRedirectToHTTP(t *testing.T) {
	var cleartextRequests atomic.Int32
	cleartext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cleartextRequests.Add(1); w.Write([]byte("unsafe")) }))
	defer cleartext.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, cleartext.URL, http.StatusFound) }))
	defer secure.Close()
	httpClient := secure.Client()
	httpClient.CheckRedirect = secureRedirect
	if err := fetch(context.Background(), httpClient, secure.URL, filepath.Join(t.TempDir(), "bad"), 1024, func(int64, int64) {}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("error %v", err)
	}
	if cleartextRequests.Load() != 0 {
		t.Fatal("redirect reached cleartext destination")
	}
}
func TestEnsureVerifiesExportAcrossDockerImageStores(t *testing.T) {
	for _, wrongExport := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongExport), func(t *testing.T) {
			f := newFixture()
			opts, _ := optionsFor(t, f)
			archive := makeArchive(f.workerEntries)
			gz, err := gzip.NewReader(bytes.NewReader(archive))
			if err != nil {
				t.Fatal(err)
			}
			exported, err := io.ReadAll(gz)
			gz.Close()
			if err != nil {
				t.Fatal(err)
			}
			if wrongExport {
				exported = []byte("unrelated content")
			}
			d := &fakeDocker{images: map[string]map[string]any{}, configID: "sha256:" + strings.Repeat("a", 64), exported: exported}
			err = ensure(context.Background(), dockerClient(t, d), testRef, nil, opts)
			if (err != nil) != wrongExport {
				t.Fatalf("error %v", err)
			}
			if wrongExport && d.tags != 0 {
				t.Fatal("unrelated content tagged")
			}
		})
	}
}

func TestEnsureVerifiesCompressedLayerDiffIDs(t *testing.T) {
	for _, badChecksum := range []bool{false, true} {
		t.Run(fmt.Sprint(badChecksum), func(t *testing.T) {
			f := newFixture()
			var manifest []map[string]any
			json.Unmarshal(f.workerEntries["manifest.json"], &manifest)
			oldLayer := manifest[0]["Layers"].([]any)[0].(string)
			var compressed bytes.Buffer
			zip := gzip.NewWriter(&compressed)
			zip.Write(f.workerEntries[oldLayer])
			zip.Close()
			layer := compressed.Bytes()
			if badChecksum {
				layer = append([]byte{}, layer...)
				layer[len(layer)-8] ^= 1
			}
			delete(f.workerEntries, oldLayer)
			newLayer := "blobs/sha256/" + checksum(layer)
			f.workerEntries[newLayer] = layer
			manifest[0]["Layers"] = []string{newLayer}
			f.workerEntries["manifest.json"] = mustJSON(manifest)
			f.refresh()
			opts, _ := optionsFor(t, f)
			d := &fakeDocker{images: map[string]map[string]any{}, configID: f.configID}
			err := ensure(context.Background(), dockerClient(t, d), testRef, nil, opts)
			if (err != nil) != badChecksum {
				t.Fatalf("error %v", err)
			}
			if badChecksum && d.loads != 0 {
				t.Fatal("damaged gzip layer reached Docker")
			}
		})
	}
}

func TestEnsureReusesNativeTagWithoutAnyDownload(t *testing.T) {
	for _, test := range []struct {
		name, arch, env, id string
		wantErr             bool
	}{
		{"valid native", "amd64", testRef, "sha256:" + strings.Repeat("1", 64), false},
		{"wrong native architecture", "arm64", testRef, "sha256:" + strings.Repeat("1", 64), true},
		{"wrong native version", "amd64", "docklane:v0.1.0", "sha256:" + strings.Repeat("1", 64), true},
		{"invalid native identity", "amd64", testRef, "invalid", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			opts, count := optionsFor(t, f)
			d := &fakeDocker{images: map[string]map[string]any{testRef + "-amd64": imageDetails(test.id, test.arch, test.env)}, configID: test.id}
			err := ensure(context.Background(), dockerClient(t, d), testRef, nil, opts)
			if (err != nil) != test.wantErr {
				t.Fatalf("error %v", err)
			}
			if count.Load() != 0 || d.loads != 0 {
				t.Fatal("local native image caused a download or load")
			}
			if test.wantErr && d.tags != 0 {
				t.Fatal("invalid native image received canonical alias")
			}
			if !test.wantErr && (d.tags != 1 || d.images[testRef]["Id"] != test.id) {
				t.Fatal("native alias was not created")
			}
		})
	}
}

func TestNativeInspectDaemonErrorDoesNotTriggerDownload(t *testing.T) {
	f := newFixture()
	opts, count := optionsFor(t, f)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, testRef+"-amd64") {
			w.WriteHeader(500)
		} else {
			w.WriteHeader(404)
		}
		io.WriteString(w, `{"message":"test daemon failure"}`)
	}))
	defer server.Close()
	docker, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.48"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	if err = ensure(context.Background(), docker, testRef, nil, opts); err == nil {
		t.Fatal("expected native inspect failure")
	}
	if count.Load() != 0 {
		t.Fatal("daemon error triggered release download")
	}
}
func TestNativeAliasDoesNotReplaceUnrelatedCanonicalTag(t *testing.T) {
	d := &fakeDocker{images: map[string]map[string]any{testRef: imageDetails("sha256:"+strings.Repeat("a", 64), "amd64", testRef)}}
	if err := ensureAlias(context.Background(), dockerClient(t, d), "sha256:"+strings.Repeat("b", 64), testRef, "amd64"); err == nil {
		t.Fatal("replaced unrelated canonical image")
	}
	if d.tags != 0 {
		t.Fatal("unrelated canonical tag was overwritten")
	}
}
