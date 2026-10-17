package workflows

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
)

const selfIdentityArchiveLimit = 64 << 10

// guardStopWriters proves which container holds this installation's random
// master key before any Docker-mode stop operation. Hostnames, image names,
// environment values and cgroup names are not identities. Several containers
// may share installation data; all matching containers are protected.
func (s *Service) guardStopWriters(ctx context.Context, writers []string) error {
	if s.app.Config.Mode != "docker" || len(writers) == 0 {
		return nil
	}
	const guidance = "mount backup sources read-only in the management container, or explicitly use file mode without database consistency guarantees"
	keyPath := filepath.Join(s.app.Config.DataDir, "master.key")
	key, err := readLocalIdentityKey(keyPath)
	if err != nil {
		return fmt.Errorf("cannot verify Docklane installation identity; %s", guidance)
	}
	expected := sha256.Sum256(key)
	clear(key)
	all, err := s.app.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot enumerate containers to protect Docklane; %s", guidance)
	}
	stop := make(map[string]bool, len(writers))
	for _, id := range writers {
		stop[id] = true
	}
	identified := false
	for _, candidate := range all {
		matching, err := s.containerHasIdentity(ctx, candidate.ID, keyPath, expected)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Archive errors and headers can contain untrusted file contents.
			// Return only the safe container ID and actionable guidance.
			return fmt.Errorf("cannot safely verify installation identity in container %s; %s", candidate.ID, guidance)
		}
		if !matching {
			continue
		}
		identified = true
		if stop[candidate.ID] {
			return fmt.Errorf("refusing to stop Docklane or a container sharing its installation data (%s); %s", candidate.ID, guidance)
		}
	}
	if !identified {
		return fmt.Errorf("cannot identify Docklane among this Engine's containers; %s", guidance)
	}
	return nil
}

func readLocalIdentityKey(name string) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != 32 {
		return nil, errors.New("invalid local installation identity")
	}
	key, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, errors.New("invalid local installation identity")
	}
	return key, nil
}

func (s *Service) containerHasIdentity(ctx context.Context, id, keyPath string, expected [sha256.Size]byte) (matching bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reader, stat, err := s.app.Docker.CopyFromContainer(ctx, id, keyPath)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, errors.New("installation identity archive unavailable")
	}
	defer reader.Close()
	if !stat.Mode.IsRegular() || stat.Size != 32 {
		return false, errors.New("unexpected installation identity metadata")
	}
	archive, err := io.ReadAll(io.LimitReader(reader, selfIdentityArchiveLimit+1))
	if err != nil || len(archive) > selfIdentityArchiveLimit {
		clear(archive)
		return false, errors.New("installation identity archive exceeds safety limits")
	}
	defer clear(archive)
	return identityArchiveMatches(archive, expected)
}

func identityArchiveMatches(archive []byte, expected [sha256.Size]byte) (matching bool, err error) {
	input := bytes.NewReader(archive)
	reader := tar.NewReader(input)
	header, err := reader.Next()
	if err != nil || header == nil || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || path.Clean(header.Name) != "master.key" || header.Size != 32 {
		return false, errors.New("unexpected installation identity archive entry")
	}
	var key [32]byte
	if _, err = io.ReadFull(reader, key[:]); err != nil {
		clear(key[:])
		return false, errors.New("incomplete installation identity archive")
	}
	actual := sha256.Sum256(key[:])
	clear(key[:])
	if _, err = reader.Next(); err != io.EOF {
		return false, errors.New("unexpected extra installation identity archive entry")
	}
	// Docker tar streams may have zero padding after their end markers.
	for input.Len() > 0 {
		b, _ := input.ReadByte()
		if b != 0 {
			return false, errors.New("unexpected installation identity archive trailer")
		}
	}
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1, nil
}
