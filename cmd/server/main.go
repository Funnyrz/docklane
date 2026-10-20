package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/docker-manager/docker-manager/internal/api"
	"github.com/docker-manager/docker-manager/internal/config"
	"github.com/docker-manager/docker-manager/internal/database"
	"github.com/docker-manager/docker-manager/internal/hostmetrics"
	"github.com/docker-manager/docker-manager/internal/platform"
	"github.com/docker-manager/docker-manager/internal/resources"
	"github.com/docker-manager/docker-manager/internal/restoreworker"
	"github.com/docker-manager/docker-manager/internal/security"
	"github.com/docker-manager/docker-manager/internal/task"
	"github.com/docker-manager/docker-manager/internal/workflows"
	"github.com/docker-manager/docker-manager/web"
	"github.com/docker/docker/client"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "restore-worker" {
		if len(os.Args) != 3 {
			slog.Error("restore worker requires a specification path")
			os.Exit(1)
		}
		if e := restoreworker.Run(os.Args[2]); e != nil {
			slog.Error("restore worker failed", "error", security.Redact(e.Error()))
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "backup-worker" {
		if e := workflows.RunWorker(os.Args[2:]); e != nil {
			slog.Error("backup worker failed", "error", security.Redact(e.Error()))
			os.Exit(1)
		}
		return
	}
	if e := serve(); e != nil {
		slog.Error("server stopped", "error", e)
		os.Exit(1)
	}
}
func serve() error {
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	lock, e := database.Lock(cfg.DataDir)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = os.MkdirAll(cfg.BackupTempDir, 0700); e != nil {
		return e
	}
	tmp, e := filepath.EvalSymlinks(cfg.BackupTempDir)
	if e != nil {
		return e
	}
	if tmp == string(filepath.Separator) {
		return fmt.Errorf("backup_temp_dir cannot resolve to filesystem root")
	}
	if e = os.Chmod(tmp, 0700); e != nil {
		return e
	}
	if e = os.Setenv("TMPDIR", tmp); e != nil {
		return e
	}
	if e = os.Setenv("RESTIC_CACHE_DIR", filepath.Join(tmp, "restic-cache")); e != nil {
		return e
	}
	db, e := database.Open(cfg.DataDir)
	if e != nil {
		return e
	}
	defer db.Close()
	cfg, e = config.ApplySavedCapabilitySettings(db, cfg)
	if e != nil {
		return e
	}
	var expectedFingerprint string
	_ = db.QueryRow("SELECT value FROM settings WHERE key='master_key_fingerprint'").Scan(&expectedFingerprint)
	if expectedFingerprint != "" {
		key, e := os.ReadFile(filepath.Join(cfg.DataDir, "master.key"))
		if e != nil {
			return fmt.Errorf("master.key missing or unreadable; restore the offline key before starting: %w", e)
		}
		if security.TokenHash(string(key)) != expectedFingerprint {
			return fmt.Errorf("master.key does not match this database; restore matching database/key pair")
		}
	}
	vault, e := security.NewVault(cfg.DataDir)
	if e != nil {
		return e
	}
	key, e := os.ReadFile(filepath.Join(cfg.DataDir, "master.key"))
	if e != nil {
		return e
	}
	if _, e = db.Exec("INSERT INTO settings(key,value) VALUES('master_key_fingerprint',?) ON CONFLICT(key) DO NOTHING", security.TokenHash(string(key))); e != nil {
		return e
	}
	docker, e := client.NewClientWithOpts(client.WithHost(cfg.DockerEndpoint), client.WithAPIVersionNegotiation())
	if e != nil {
		return e
	}
	defer docker.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	manager := task.New(db)
	app := &platform.App{DB: db, Docker: docker, Tasks: manager, Vault: vault, Config: cfg}
	cpu := hostmetrics.NewCPUSampler(hostmetrics.SupportsLocalHost(runtime.GOOS, cfg.Mode, cfg.DockerEndpoint))
	app.CPU = cpu
	cpu.Start(ctx)
	defer func() { cancel(); cpu.Wait() }()
	_, e = db.Exec("INSERT INTO hosts(id,name,docker_endpoint) VALUES('local','Local Docker',?) ON CONFLICT(id) DO UPDATE SET docker_endpoint=excluded.docker_endpoint", cfg.DockerEndpoint)
	if e != nil {
		return e
	}
	router := api.New(app, web.Assets(), resources.Register, workflows.Register)
	if e = manager.Start(ctx); e != nil {
		return e
	}
	go func() {
		if e := manager.WaitReady(ctx); e == nil {
			if e = workflows.Start(ctx, app); e != nil {
				slog.Error("scheduler failed", "error", e)
			}
		}
	}()
	go api.Monitor(ctx, app)
	server := &http.Server{Addr: cfg.Listen, Handler: router, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	stopped := make(chan error, 1)
	go func() {
		slog.Info("Docklane started", "listen", cfg.Listen, "mode", cfg.Mode)
		stopped <- server.ListenAndServe()
	}()
	select {
	case e = <-stopped:
		cancel()
	case <-ctx.Done():
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
	cancel()
	manager.Wait()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
