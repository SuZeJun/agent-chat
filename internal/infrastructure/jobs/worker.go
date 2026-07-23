package jobs

import (
	"context"
	"log/slog"
	"time"
)

// DatabaseHealth 定义 Worker 检查数据库可用性所需的最小能力。
type DatabaseHealth interface {
	Ping(context.Context) error
}

// Worker 管理后台任务进程的轮询生命周期。
//
// 当前脚手架阶段仅执行数据库心跳，持久化 Job 的领取和处理将在后续里程碑实现。
type Worker struct {
	logger       *slog.Logger
	database     DatabaseHealth
	pollInterval time.Duration
	pingTimeout  time.Duration
}

// NewWorker 使用数据库健康检查和轮询参数创建 Worker。
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

// Run 启动轮询循环，并在 Context 取消时停止。
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
