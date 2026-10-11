package app

import (
	"context"
	"log/slog"
	"time"
)

func RunEvery(
	ctx context.Context,
	logger *slog.Logger,
	name string,
	interval time.Duration,
	run func(context.Context) error,
) {
	runEvery(ctx, logger, job{name: name, interval: interval, run: run})
}
