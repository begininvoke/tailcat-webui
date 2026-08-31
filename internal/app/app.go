package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/config"
	"github.com/ca-x/tailcat-webui/internal/database"
	"github.com/ca-x/tailcat-webui/internal/httpapi"
	"github.com/ca-x/tailcat-webui/internal/publish"
	"github.com/ca-x/tailcat-webui/internal/secrets"
	"github.com/ca-x/tailcat-webui/internal/tailnet"
	"github.com/ca-x/tailcat-webui/webdist"

	"github.com/gofrs/flock"
	"github.com/labstack/echo/v5"
)

type App struct {
	cfg     config.Config
	logger  *slog.Logger
	db      *ent.Client
	tailnet *tailnet.Manager
	publish *publish.Service
	lock    *flock.Flock
	handler http.Handler
}

func New(ctx context.Context, logger *slog.Logger) (*App, error) {
	if logger == nil {
		return nil, errors.New("app: nil logger")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := config.EnsureRuntimeDirs(cfg); err != nil {
		return nil, err
	}
	processLock := flock.New(filepath.Join(cfg.DataDir, "tailcat-webui.lock"))
	locked, err := processLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire process lock: %w", err)
	}
	if !locked {
		return nil, errors.New("another Tailcat WebUI process is already using this data directory")
	}
	releaseLock := true
	defer func() {
		if releaseLock {
			_ = processLock.Unlock()
		}
	}()
	db, _, err := database.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return nil, err
	}
	if err := config.SecureRuntimeFiles(cfg); err != nil {
		db.Close()
		return nil, err
	}
	box, err := secrets.NewBox(cfg.MasterKey)
	if err != nil {
		db.Close()
		return nil, err
	}
	authService, err := auth.NewService(ctx, db, cfg, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	auditService, err := audit.NewService(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	manager, err := tailnet.NewManager(db, box, tailnet.NewTargetPolicy(cfg.MappingTargets), tailnet.NewTargetPolicy(cfg.ExitTargets), cfg.AllowedDERPHosts, cfg.UnsafeSSH, func(ctx context.Context, event tailnet.Event) error {
		outcome := "success"
		if event.State == "error" {
			outcome = "failure"
		}
		return auditService.Record(ctx, audit.Entry{UserID: event.UserID, Action: "runtime." + string(event.State), ResourceKind: event.ResourceKind, ResourceID: event.ResourceID, Outcome: outcome})
	}, logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	publisher, err := publish.NewService(db, manager, cfg.BaseURL, cfg.PublishURL, cfg.MasterKey, cfg.SessionIdle, logger)
	if err != nil {
		manager.Close()
		db.Close()
		return nil, err
	}
	web, err := fs.Sub(webdist.Files, "dist")
	if err != nil {
		publisher.Close()
		manager.Close()
		db.Close()
		return nil, fmt.Errorf("open embedded web assets: %w", err)
	}
	api, err := httpapi.New(db, authService, auditService, manager, publisher, cfg, logger, web)
	if err != nil {
		publisher.Close()
		manager.Close()
		db.Close()
		return nil, err
	}
	handler, err := api.Handler()
	if err != nil {
		publisher.Close()
		manager.Close()
		db.Close()
		return nil, err
	}
	csrf := http.NewCrossOriginProtection()
	// Published resources live on a separate origin and enforce their own CORS
	// and cookie policy. Management routes remain protected.
	csrf.AddInsecureBypassPattern("/r/")
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "cross-origin request denied", http.StatusForbidden)
	}))
	releaseLock = false
	return &App{cfg: cfg, logger: logger, db: db, tailnet: manager, publish: publisher, lock: processLock, handler: csrf.Handler(handler)}, nil
}

func (a *App) Run(ctx context.Context) error {
	restoreCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := a.tailnet.Restore(restoreCtx); err != nil {
		a.logger.WarnContext(ctx, "Some Tailcat servers could not be restored", "error", err)
	}
	a.logger.InfoContext(ctx, "Tailcat WebUI listening", "address", a.cfg.Addr, "base_url", a.cfg.BaseURL.String())
	server := echo.StartConfig{
		Address: a.cfg.Addr, HideBanner: true, HidePort: true, GracefulTimeout: 10 * time.Second,
		BeforeServeFunc: func(server *http.Server) error {
			server.ReadHeaderTimeout = 10 * time.Second
			server.IdleTimeout = 90 * time.Second
			server.MaxHeaderBytes = 1 << 20
			return nil
		},
	}
	return server.Start(ctx, a.handler)
}

func (a *App) Close() error {
	a.publish.Close()
	return errors.Join(a.tailnet.Close(), a.db.Close(), a.lock.Unlock())
}

func DefaultLogger() *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("TAILCAT_WEBUI_LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
