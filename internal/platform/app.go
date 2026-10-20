package platform

import (
	"database/sql"
	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/hostmetrics"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker/docker/client"
)

// App contains shared adapters. Resource state must always be read from Docker.
type App struct {
	DB     *sql.DB
	Docker *client.Client
	Tasks  *task.Manager
	Vault  *security.Vault
	Config config.Config
	CPU    interface {
		Snapshot() hostmetrics.CPUSnapshot
	}
}
