package workerimage

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

const maxExpanded = int64(2 << 30)
const maxMetadata = int64(4 << 20)

type member struct {
	digest     string
	diffDigest string
	size       int64
	data       []byte
}

func safePath(name string) bool {
	return name != "" && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") && path.Clean(name) == strings.TrimSuffix(name, "/") && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

// scanArchive never extracts paths. Limits apply to the complete expanded tar,
// member count and metadata retained in memory, including malicious tar headers.
func scanArchive(ctx context.Context, reader io.Reader, keep func(string) bool) (map[string]member, error) {
	archive := tar.NewReader(contextReader{ctx, reader})
	members := make(map[string]member)
	var total, retained, inflated int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			// Docker must not see a second tar stream that validation skipped.
			rest := make([]byte, 32<<10)
			for {
				count, tailErr := reader.Read(rest)
				for _, value := range rest[:count] {
					if value != 0 {
						return nil, errors.New("nonzero data after archive terminator")
					}
				}
				if tailErr == io.EOF {
					return members, nil
				}
				if tailErr != nil {
					return nil, tailErr
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
		}
		if err != nil {
			return nil, err
		}
		if !safePath(header.Name) {
			return nil, fmt.Errorf("unsafe archive path %q", header.Name)
		}
		name := strings.TrimSuffix(header.Name, "/")
		if _, duplicate := members[name]; duplicate {
			return nil, fmt.Errorf("duplicate archive path %q", name)
		}
		if len(members) >= 100000 {
			return nil, errors.New("too many archive members")
		}
		if header.Size < 0 || header.Size > maxExpanded-total {
			return nil, errors.New("archive exceeds expanded size limit")
		}
		total += header.Size
		item := member{size: header.Size}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return nil, errors.New("invalid archive directory")
			}
		case tar.TypeReg, tar.TypeRegA:
			hash := sha256.New()
			var raw io.Reader = io.TeeReader(archive, hash)
			var metadata bytes.Buffer
			if keep(name) && header.Size <= maxMetadata {
				if header.Size > (32<<20)-retained {
					return nil, errors.New("archive metadata exceeds limit")
				}
				raw = io.TeeReader(raw, &metadata)
				retained += header.Size
			}
			buffered := bufio.NewReader(raw)
			prefix, _ := buffered.Peek(2)
			if len(prefix) == 2 && prefix[0] == 0x1f && prefix[1] == 0x8b {
				// containerd exports compressed OCI layers; rootfs.diff_ids
				// identifies their uncompressed tar while blob names hash gzip.
				decompressed, gzipErr := gzip.NewReader(buffered)
				if gzipErr != nil {
					return nil, gzipErr
				}
				diffHash := sha256.New()
				count, copyErr := io.Copy(diffHash, io.LimitReader(contextReader{ctx, decompressed}, maxExpanded-inflated+1))
				decompressed.Close()
				if copyErr != nil {
					return nil, copyErr
				}
				inflated += count
				if inflated > maxExpanded {
					return nil, errors.New("compressed worker layers exceed expanded size limit")
				}
				item.diffDigest = hex.EncodeToString(diffHash.Sum(nil))
			}
			if _, err = io.Copy(io.Discard, buffered); err != nil {
				return nil, err
			}
			item.digest = hex.EncodeToString(hash.Sum(nil))
			if item.diffDigest == "" {
				item.diffDigest = item.digest
			}
			item.data = metadata.Bytes()
			if strings.HasPrefix(name, "blobs/sha256/") && strings.TrimPrefix(name, "blobs/sha256/") != item.digest {
				return nil, errors.New("worker blob digest mismatch")
			}
		default:
			return nil, fmt.Errorf("unsupported archive entry %q", name)
		}
		members[name] = item
	}
}
func installationMetadata(ctx context.Context, compressed, version, arch, asset, assetDigest string) (string, error) {
	file, err := os.Open(compressed)
	if err != nil {
		return "", err
	}
	defer file.Close()
	zipped, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer zipped.Close()
	limited := &io.LimitedReader{R: contextReader{ctx, zipped}, N: 64<<20 + 1}
	entries, err := scanArchive(ctx, limited, func(name string) bool { return name == "VERSION" || name == "IMAGE-SHA256SUMS" || name == "IMAGES.txt" })
	if err != nil {
		return "", fmt.Errorf("installation archive: %w", err)
	}
	if _, err = io.Copy(io.Discard, limited); err != nil {
		return "", err
	}
	if limited.N == 0 {
		return "", errors.New("installation archive exceeds size limit")
	}
	if strings.TrimSpace(string(entries["VERSION"].data)) != version {
		return "", errors.New("installation material version mismatch")
	}
	sums, err := parseSums(entries["IMAGE-SHA256SUMS"].data)
	if err != nil {
		return "", err
	}
	if sums[asset] != assetDigest {
		return "", errors.New("worker inner and outer checksums disagree")
	}
	imageLine := regexp.MustCompile(`^\[([^]]+)\] (sha256:[a-f0-9]{64}) linux/(amd64|arm64)$`)
	expected := ""
	for _, line := range strings.Split(strings.TrimSpace(string(entries["IMAGES.txt"].data)), "\n") {
		match := imageLine.FindStringSubmatch(line)
		if match == nil {
			return "", errors.New("invalid worker identity manifest")
		}
		if match[1] == "docklane:v"+version+"-"+arch {
			if expected != "" || match[3] != arch {
				return "", errors.New("duplicate or mismatched native worker identity")
			}
			expected = match[2]
		}
	}
	if expected == "" {
		return "", errors.New("native worker identity is missing")
	}
	return expected, nil
}

type verifiedImage struct{ configID string }

func verifyWorker(ctx context.Context, compressed, unpacked, reference, arch, expected string) (verifiedImage, error) {
	result := verifiedImage{}
	file, err := os.Open(compressed)
	if err != nil {
		return result, err
	}
	defer file.Close()
	zipped, err := gzip.NewReader(file)
	if err != nil {
		return result, err
	}
	defer zipped.Close()
	output, err := os.OpenFile(unpacked, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return result, err
	}
	defer output.Close()
	n, err := io.Copy(output, io.LimitReader(contextReader{ctx, zipped}, maxExpanded+1))
	if err != nil {
		return result, err
	}
	if n > maxExpanded {
		return result, errors.New("worker archive exceeds expanded size limit")
	}
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	return verifyTar(ctx, output, reference, arch, expected)
}

func verifyTar(ctx context.Context, input io.Reader, reference, arch, expected string) (verifiedImage, error) {
	result := verifiedImage{}
	members, err := scanArchive(ctx, input, func(name string) bool {
		return name == "manifest.json" || name == "repositories" || strings.HasSuffix(name, ".json") || strings.HasPrefix(name, "blobs/sha256/")
	})
	if err != nil {
		return result, err
	}
	var manifest []struct {
		Config   string
		RepoTags []string
		Layers   []string
	}
	if err = json.Unmarshal(members["manifest.json"].data, &manifest); err != nil {
		return result, errors.New("invalid worker archive manifest")
	}
	native := reference + "-" + arch
	if len(manifest) != 1 || len(manifest[0].RepoTags) != 1 || manifest[0].RepoTags[0] != native {
		return result, errors.New("worker archive must contain only the current architecture tag")
	}
	indexRoot := ""
	if index, present := members["index.json"]; present {
		var roots struct {
			Manifests []struct {
				Digest      string            `json:"digest"`
				Annotations map[string]string `json:"annotations"`
			} `json:"manifests"`
		}
		if json.Unmarshal(index.data, &roots) != nil || len(roots.Manifests) != 1 {
			return result, errors.New("worker OCI index must contain only one native image")
		}
		root := roots.Manifests[0]
		for key, value := range root.Annotations {
			if key == "io.containerd.image.name" && value != native && value != "docker.io/library/"+native {
				return result, errors.New("worker OCI index contains unrelated image name")
			}
			if key == "org.opencontainers.image.ref.name" && value != strings.TrimPrefix(native, "docklane:") {
				return result, errors.New("worker OCI index contains unrelated image tag")
			}
		}
		indexRoot = root.Digest
	}
	if repositories, present := members["repositories"]; present {
		var tags map[string]map[string]string
		if json.Unmarshal(repositories.data, &tags) != nil || len(tags) != 1 {
			return result, errors.New("invalid legacy worker repository tags")
		}
		for repo, names := range tags {
			if (repo != "docklane" && repo != "docker.io/library/docklane") || len(names) != 1 {
				return result, errors.New("unrelated legacy worker repository")
			}
			for tag := range names {
				if tag != strings.TrimPrefix(native, "docklane:") {
					return result, errors.New("unrelated legacy worker tag")
				}
			}
		}
	}
	selected := manifest[0]
	if !safePath(selected.Config) {
		return result, errors.New("unsafe worker config path")
	}
	config, ok := members[selected.Config]
	if !ok || len(config.data) == 0 {
		return result, errors.New("worker config is missing or too large")
	}
	result.configID = "sha256:" + config.digest
	if indexRoot != "" {
		if (expected != result.configID && indexRoot != expected) || !identityReferences(members, indexRoot, result.configID, 0, make(map[string]bool)) {
			return result, errors.New("worker OCI index identity mismatch")
		}
	}
	if !identityReferences(members, expected, result.configID, 0, make(map[string]bool)) {
		return result, errors.New("worker identity does not match release metadata")
	}
	var configuration struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Config       struct {
			Env []string `json:"Env"`
		} `json:"config"`
		RootFS struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err = json.Unmarshal(config.data, &configuration); err != nil {
		return result, err
	}
	if configuration.OS != "linux" || configuration.Architecture != arch {
		return result, errors.New("worker archive architecture mismatch")
	}
	versionEnv := []string{}
	for _, entry := range configuration.Config.Env {
		if strings.HasPrefix(entry, "DM_BACKUP_WORKER_IMAGE=") {
			versionEnv = append(versionEnv, entry)
		}
	}
	if len(versionEnv) != 1 || versionEnv[0] != "DM_BACKUP_WORKER_IMAGE="+reference {
		return result, errors.New("worker archive application version mismatch")
	}
	if configuration.RootFS.Type != "layers" || len(configuration.RootFS.DiffIDs) != len(selected.Layers) {
		return result, errors.New("invalid worker layer configuration")
	}
	for index, layer := range selected.Layers {
		entry, present := members[layer]
		if !safePath(layer) || !present || entry.digest == "" || "sha256:"+entry.diffDigest != configuration.RootFS.DiffIDs[index] {
			return result, errors.New("worker layer content mismatch")
		}
	}
	return result, nil
}
func identityReferences(members map[string]member, identity, config string, depth int, seen map[string]bool) bool {
	if !digestPattern.MatchString(identity) || depth > 8 || seen[identity] {
		return false
	}
	if identity == config {
		return true
	}
	seen[identity] = true
	entry, ok := members["blobs/sha256/"+strings.TrimPrefix(identity, "sha256:")]
	if !ok || "sha256:"+entry.digest != identity {
		return false
	}
	var descriptor struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if json.Unmarshal(entry.data, &descriptor) != nil {
		return false
	}
	if descriptor.Config.Digest == config {
		return true
	}
	for _, child := range descriptor.Manifests {
		if identityReferences(members, child.Digest, config, depth+1, seen) {
			return true
		}
	}
	return false
}
