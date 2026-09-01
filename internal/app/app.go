package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/internal/audit"
	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/config"
	"github.com/ca-x/tailcat-webui/internal/database"
	"github.com/ca-x/tailcat-webui/internal/diagnostics"
	"github.com/ca-x/tailcat-webui/internal/httpapi"
	"github.com/ca-x/tailcat-webui/internal/publish"
	"github.com/ca-x/tailcat-webui/internal/secrets"
	"github.com/ca-x/tailcat-webui/internal/tailnet"
	"github.com/ca-x/tailcat-webui/internal/transfer"
	"github.com/ca-x/tailcat-webui/webdist"

	"github.com/gofrs/flock"
	"github.com/labstack/echo/v5"
)

type App struct {
	cfg             config.Config
	logger          *slog.Logger
	db              *ent.Client
	diagnostics     *diagnostics.Service
	transfer        *transfer.Service
	transferStorage *transfer.Storage
	tailnet         *tailnet.Manager
	publish         *publish.Service
	lock            *flock.Flock
	handler         http.Handler
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
	if !box.Available() {
		// Loopback demo mode intentionally permits an omitted environment key.
		// Keep a private local key so encrypted transfer jobs still resume after
		// a demo-process restart.
		demoKey, err := loadOrCreateDemoSecretKey(cfg.DataDir)
		if err != nil {
			db.Close()
			return nil, err
		}
		box, err = secrets.NewBox(demoKey)
		clear(demoKey)
		if err != nil {
			db.Close()
			return nil, err
		}
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
	diagnosticService, err := diagnostics.NewService(ctx, db, manager, auditService, manager, logger)
	if err != nil {
		manager.Close()
		db.Close()
		return nil, err
	}
	transferConfig := cfg.EffectiveTransfer()
	transferStorage, err := transfer.NewStorageWithLimits(filepath.Join(cfg.DataDir, "transfers"), transfer.StorageLimits{
		MaxFileBytes: transferConfig.MaxFileBytes, MaxScopeBytes: max(transferConfig.MaxShareBytes, transferConfig.MaxJobBytes),
		MaxOwnerBytes: transferConfig.MaxOwnerBytes, MaxFilesPerScope: transferConfig.MaxFilesPerShare,
	})
	if err != nil {
		diagnosticService.Close()
		manager.Close()
		db.Close()
		return nil, err
	}
	transferService, err := transfer.NewServiceWithLimits(ctx, db, transferStorage, box, manager, auditService, manager, logger, transfer.ServiceLimits{
		MaxFileBytes: transferConfig.MaxFileBytes, MaxShareBytes: transferConfig.MaxShareBytes,
		MaxJobBytes: transferConfig.MaxJobBytes, MaxFilesPerShare: transferConfig.MaxFilesPerShare,
		Workers: transferConfig.Workers, MaxJobsPerOwner: transferConfig.MaxJobsPerOwner, Expiry: transferConfig.Expiry,
	})
	if err != nil {
		transferStorage.Close()
		diagnosticService.Close()
		manager.Close()
		db.Close()
		return nil, err
	}
	if err := manager.RegisterReservedTCPHandler(transfer.ReservedPort, func(serverID string) tailnet.TCPHandler {
		return tailnet.TCPHandler(transferService.ReservedHandler(serverID))
	}); err != nil {
		transferService.Close()
		transferStorage.Close()
		diagnosticService.Close()
		manager.Close()
		db.Close()
		return nil, err
	}
	publisher, err := publish.NewService(db, manager, cfg.BaseURL, cfg.PublishURL, cfg.MasterKey, cfg.SessionIdle, logger)
	if err != nil {
		transferService.Close()
		transferStorage.Close()
		diagnosticService.Close()
		manager.Close()
		db.Close()
		return nil, err
	}
	web, err := fs.Sub(webdist.Files, "dist")
	if err != nil {
		publisher.Close()
		transferService.Close()
		transferStorage.Close()
		diagnosticService.Close()
		manager.Close()
		db.Close()
		return nil, fmt.Errorf("open embedded web assets: %w", err)
	}
	api, err := httpapi.New(db, authService, auditService, diagnosticService, manager, publisher, transferService, cfg, logger, web)
	if err != nil {
		publisher.Close()
		transferService.Close()
		transferStorage.Close()
		diagnosticService.Close()
		manager.Close()
		db.Close()
		return nil, err
	}
	handler, err := api.Handler()
	if err != nil {
		publisher.Close()
		transferService.Close()
		transferStorage.Close()
		diagnosticService.Close()
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
	return &App{cfg: cfg, logger: logger, db: db, diagnostics: diagnosticService, transfer: transferService, transferStorage: transferStorage, tailnet: manager, publish: publisher, lock: processLock, handler: csrf.Handler(handler)}, nil
}

func loadOrCreateDemoSecretKey(dataDir string) ([]byte, error) {
	keyPath := filepath.Join(dataDir, ".tailcat-webui-demo-key")
	before, err := os.Lstat(keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate demo secret key: %w", err)
		}
		file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			clear(key)
			return nil, fmt.Errorf("create demo secret key: %w", err)
		}
		written, writeErr := file.Write(key)
		if writeErr == nil && written != len(key) {
			writeErr = io.ErrShortWrite
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			clear(key)
			removeErr := os.Remove(keyPath)
			return nil, errors.Join(fmt.Errorf("persist demo secret key: %w", err), removeErr)
		}
		if runtime.GOOS != "windows" {
			directory, err := os.Open(dataDir)
			if err != nil {
				clear(key)
				return nil, fmt.Errorf("open demo key directory: %w", err)
			}
			if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
				clear(key)
				return nil, fmt.Errorf("sync demo key directory: %w", err)
			}
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect demo secret key: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || runtime.GOOS != "windows" && before.Mode().Perm() != 0o600 {
		return nil, errors.New("demo secret key has unsafe type or permissions")
	}
	file, err := os.Open(keyPath)
	if err != nil {
		return nil, fmt.Errorf("open demo secret key: %w", err)
	}
	key, readErr := io.ReadAll(io.LimitReader(file, 33))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, afterErr := os.Lstat(keyPath)
	if err := errors.Join(readErr, statErr, closeErr, afterErr); err != nil {
		clear(key)
		return nil, fmt.Errorf("read demo secret key: %w", err)
	}
	if len(key) != 32 || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || runtime.GOOS != "windows" && after.Mode().Perm() != 0o600 || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		clear(key)
		return nil, errors.New("demo secret key changed while opening")
	}
	return key, nil
}

func (a *App) Run(ctx context.Context) error {
	restoreCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := a.tailnet.Restore(restoreCtx); err != nil {
		a.logger.WarnContext(ctx, "Some Tailcat servers could not be restored", "error", err)
	}
	if err := a.transfer.RecoverAfterRestore(restoreCtx); err != nil {
		a.logger.WarnContext(ctx, "Some transfers could not be recovered", "error", err)
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
	return errors.Join(closeTransferServicesBeforeTailnet(a.publish, a.transfer, a.transferStorage, a.diagnostics, a.tailnet), a.db.Close(), a.lock.Unlock())
}

type publishedCloser interface{ Close() }
type diagnosticsCloser interface{ Close() error }
type tailnetCloser interface{ Close() error }

func closeServicesBeforeTailnet(publisher publishedCloser, diagnostics diagnosticsCloser, manager tailnetCloser) error {
	publisher.Close()
	diagnosticErr := diagnostics.Close()
	return errors.Join(diagnosticErr, manager.Close())
}

func closeTransferServicesBeforeTailnet(publisher publishedCloser, transferService, storage, diagnostics diagnosticsCloser, manager tailnetCloser) error {
	publisher.Close()
	transferErr := transferService.Close()
	storageErr := storage.Close()
	diagnosticErr := diagnostics.Close()
	return errors.Join(transferErr, storageErr, diagnosticErr, manager.Close())
}

func DefaultLogger() *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("TAILCAT_WEBUI_LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
