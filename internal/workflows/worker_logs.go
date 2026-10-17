package workflows

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// Capture bounded, redacted stderr before removing a failed worker. Neither the
// command environment nor plaintext manifests are emitted to the task stream.
func (s *Service) workerFailure(t *task.Run, id, reason, secret string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logs, e := s.app.Docker.ContainerLogs(ctx, id, container.LogsOptions{ShowStderr: true, Tail: "50"})
	if e != nil {
		t.Log("Worker stderr unavailable: " + security.Redact(e.Error()))
		return errors.New(reason)
	}
	defer logs.Close()
	var stderr limitedBuffer
	_, _ = stdcopy.StdCopy(io.Discard, &stderr, io.LimitReader(logs, 256<<10))
	detail := stderr.String()
	if len(detail) > 8192 {
		detail = detail[len(detail)-8192:]
	}
	if secret != "" {
		detail = strings.ReplaceAll(detail, secret, "[REDACTED]")
	}
	detail = security.Redact(detail)
	if strings.TrimSpace(detail) != "" {
		t.Log("Worker stderr: " + detail)
	}
	return errors.New(reason + "; inspect sanitized worker stderr in task logs")
}
