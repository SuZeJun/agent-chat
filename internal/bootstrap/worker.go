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

	worker, err := jobs.NewWorker(jobs.WorkerOptions{
		Logger:         runtime.logger,
		Queue:          jobs.NewPostgresQueue(runtime.database),
		Handlers:       map[string]jobs.Handler{},
		WorkerID:       runtime.config.Worker.ID,
		PollInterval:   runtime.config.Worker.PollInterval,
		JobTimeout:     runtime.config.Worker.JobTimeout,
		LockTimeout:    runtime.config.Worker.LockTimeout,
		RetryBaseDelay: runtime.config.Worker.RetryBaseDelay,
		RetryMaxDelay:  runtime.config.Worker.RetryMaxDelay,
	})
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}
