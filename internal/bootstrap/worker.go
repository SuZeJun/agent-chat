package bootstrap

import (
	"context"
	"io"

	"agent-chat/internal/infrastructure/jobs"
)

func RunWorker(ctx context.Context, output io.Writer) error {
	runtime, err := newRuntime(ctx, output)
	if err != nil {
		return err
	}
	defer runtime.close()

	worker := jobs.NewWorker(
		runtime.logger,
		runtime.database,
		runtime.config.Worker.PollInterval,
		runtime.config.Database.PingTimeout,
	)
	return worker.Run(ctx)
}
