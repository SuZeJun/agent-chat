package bootstrap

import (
	"context"
	"io"

	"agent-chat/internal/infrastructure/jobs"
)

// RunWorker 组装并运行后台 Worker，直到 Context 被取消或启动失败。
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
