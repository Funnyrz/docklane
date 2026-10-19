package workflows

import (
	"fmt"

	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker-manager/docker-manager/internal/workerimage"
)

func backupNeedsWorker(mode string, sources []backupSource) bool {
	if mode == "docker" {
		return true
	}
	for _, source := range sources {
		if source.Volume != "" {
			return true
		}
	}
	return false
}

func (s *Service) prepareWorker(t *task.Run) error {
	t.Progress("prepare_worker_image", 7)
	if err := workerimage.Ensure(t.Context(), s.app.Docker, s.app.Config.BackupWorkerImage, t.Log); err != nil {
		return fmt.Errorf("prepare backup/restore worker image (retry the task after resolving connectivity, disk space or image availability): %w", err)
	}
	return nil
}
