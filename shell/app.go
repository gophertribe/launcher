package shell

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type ExecutionStage interface {
	Name() string
	Configure(ctx context.Context, cancel context.CancelFunc) (context.Context, error)
	Run(ctx context.Context) (ExecutionStage, error)
	Shutdown(ctx context.Context, timeout context.CancelFunc) error
	ShutdownTimeout() time.Duration
}

func Execute(globalCtx context.Context, current ExecutionStage) error {
	for {
		slog.Info("entering execution stage", "stage", current.Name())
		next, err := executeStage(globalCtx, current)
		if err != nil {
			return fmt.Errorf("error executing stage %s: %w", current.Name(), err)
		}
		if next == nil {
			return nil
		}
		if errors.Is(globalCtx.Err(), context.Canceled) {
			// global context was canceled, we are done
			return nil
		}
		// switch to the next stage
		current = next
	}
}

func executeStage(globalCtx context.Context, current ExecutionStage) (ExecutionStage, error) {
	// we assign a separate context for each stage
	ctx, cancel := context.WithCancel(globalCtx)
	defer cancel()

	now := time.Now()
	defer func() {
		// we attempt the shutdown
		shutdownErr := current.Shutdown(context.WithTimeout(context.Background(), current.ShutdownTimeout()))
		if shutdownErr != nil {
			slog.Error("error during shutdown", "stage", current.Name(), "err", shutdownErr)
			return
		}
		slog.Info("shutdown completed", "stage", current.Name(), "uptime", time.Since(now))
	}()

	// we initialize a separate cancelable context for each stage
	ctx, err := current.Configure(ctx, cancel)
	if err != nil {
		return nil, fmt.Errorf("%s > error during config: %w", current.Name(), err)
	}
	slog.Info("configuration completed", "stage", current.Name(), "uptime", time.Since(now))

	next, err := current.Run(ctx)
	// canceled context error would typically concern the global context, so it is an expected error
	if err != nil && !errors.Is(err, context.Canceled) {
		return nil, fmt.Errorf("%s > error during runtime: %w", current.Name(), err)
	}
	slog.Info("runtime exit", "stage", current.Name(), "uptime", time.Since(now))
	return next, nil
}
