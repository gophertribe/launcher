package shell

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime/pprof"
)

func Shutdown(ctx context.Context, cancel context.CancelFunc, shutdown func(cancelFunc context.CancelFunc)) error {
	// when shutdown ends, it will cancel the context
	go shutdown(cancel)

	// this context will cancel either when the shutdown procedure is over or when the timeout expires
	<-ctx.Done()
	// canceled context is fine here
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	err := pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
	if err != nil {
		slog.Error("error printing goroutine profile", "err", err)
	}
	return ctx.Err()
}
