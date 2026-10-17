package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const verifiedSnapshotTag = "dm-verified:v1"
const unverifiedSnapshotWarning = "This snapshot has no repository verification-complete marker. It may be partial, interrupted, or from an older installation. Only explicitly acknowledged rescue into a new directory is allowed; overwrite and service recreation are prohibited."

func snapshotVerification(tags []string) (string, string) {
	for _, tag := range tags {
		if tag == verifiedSnapshotTag {
			return "verified", ""
		}
	}
	return "unverified", unverifiedSnapshotWarning
}

func (s *Service) snapshotVerification(ctx context.Context, r Repository, ref string) (string, string, error) {
	if !validDockerID(ref) {
		return "", "", errors.New("immutable snapshot ID required")
	}
	b, err := s.restic(ctx, r, "cat", "snapshot", ref)
	if err != nil {
		return "", "", err
	}
	var snapshot struct {
		Tags []string `json:"tags"`
	}
	if err = json.Unmarshal(b, &snapshot); err != nil {
		return "", "", err
	}
	status, warning := snapshotVerification(snapshot.Tags)
	return status, warning, nil
}

// Restic tag changes the encrypted snapshot's identity. Consume its documented
// changed JSON record and verify the new ID instead of keeping the backup ID.
func (s *Service) markSnapshotVerified(ctx context.Context, r Repository, ref string) (string, error) {
	b, err := s.restic(ctx, r, "tag", "--json", "--add", verifiedSnapshotTag, ref)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	newRef := ""
	for {
		var message struct {
			Type string `json:"message_type"`
			Old  string `json:"old_snapshot_id"`
			New  string `json:"new_snapshot_id"`
		}
		if err = decoder.Decode(&message); err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}
		if message.Type == "changed" && message.Old == ref {
			if newRef != "" || !validDockerID(message.New) {
				return "", errors.New("invalid Restic tag snapshot identity response")
			}
			newRef = message.New
		}
	}
	if newRef == "" {
		return "", errors.New("Restic tag did not return a changed snapshot identity")
	}
	status, _, err := s.snapshotVerification(ctx, r, newRef)
	if err != nil {
		return "", err
	}
	if status != "verified" {
		return "", fmt.Errorf("snapshot verification marker was not persisted: %s", newRef)
	}
	return newRef, nil
}
