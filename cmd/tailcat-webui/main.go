package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ca-x/tailcat-webui/internal/app"
)

func main() {
	logger := app.DefaultLogger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	application, err := app.New(startupCtx, logger)
	cancel()
	if err != nil {
		logger.Error("Application setup failed", "error", err)
		os.Exit(1)
	}
	runErr := application.Run(ctx)
	closeErr := application.Close()
	if err := errors.Join(runErr, closeErr); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("Application stopped with an error", slog.Any("error", err))
		os.Exit(1)
	}
}
