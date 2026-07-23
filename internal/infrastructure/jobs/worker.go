package jobs

import (
	"context"
	"log/slog"
	"time"
)

type DatabaseHealth interface {
	Ping(context.Context) error
}

type Worker struct {
	logger       *slog.Logger
	database     DatabaseHealth
	pollInterval time.Duration
	pingTimeout  time.Duration
}

func NewWorker(
	logger *slog.Logger,
	database DatabaseHealth,
	pollInterval time.Duration,
	pingTimeout time.Duration,
) *Worker {
	return &Worker{
		logger:       logger,
		database:     database,
		pollInterval: pollInterval,
		pingTimeout:  pingTimeout,
	}
}

func (worker *Worker) Run(ctx context.Context) error {
	worker.logger.InfoContext(ctx, "worker started", "poll_interval", worker.pollInterval.String())
	ticker := time.NewTicker(worker.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			worker.logger.Info("worker stopped")
			return nil
		case <-ticker.C:
			pingContext, cancel := context.WithTimeout(ctx, worker.pingTimeout)
			err := worker.database.Ping(pingContext)
			cancel()
			if err != nil {
				worker.logger.WarnContext(ctx, "worker database health check failed", "error", err)
				continue
			}
			worker.logger.DebugContext(ctx, "worker heartbeat")
		}
	}
}
