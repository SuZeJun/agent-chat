package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"
)

const finalizeTimeout = 5 * time.Second

// WorkerOptions 定义持久化任务执行器的依赖和可靠性参数。
type WorkerOptions struct {
	Logger         *slog.Logger
	Queue          Queue
	Handlers       map[string]Handler
	WorkerID       string
	PollInterval   time.Duration
	JobTimeout     time.Duration
	LockTimeout    time.Duration
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
}

// Worker 轮询已注册类型的持久化任务，并管理执行、重试和锁超时恢复。
type Worker struct {
	logger         *slog.Logger
	queue          Queue
	handlers       map[string]Handler
	jobTypes       []string
	workerID       string
	pollInterval   time.Duration
	jobTimeout     time.Duration
	lockTimeout    time.Duration
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration
}

// NewWorker 创建持久化任务 Worker，并拒绝不完整或不安全的运行参数。
func NewWorker(options WorkerOptions) (*Worker, error) {
	if options.Logger == nil {
		return nil, errors.New("worker logger is required")
	}
	if options.Queue == nil {
		return nil, errors.New("worker queue is required")
	}
	if options.PollInterval <= 0 || options.JobTimeout <= 0 || options.LockTimeout <= 0 {
		return nil, errors.New("worker intervals and timeouts must be greater than zero")
	}
	if options.RetryBaseDelay <= 0 || options.RetryMaxDelay < options.RetryBaseDelay {
		return nil, errors.New("worker retry delays are invalid")
	}

	workerID := strings.TrimSpace(options.WorkerID)
	if workerID == "" {
		var err error
		workerID, err = generateWorkerID()
		if err != nil {
			return nil, err
		}
	}
	if len(workerID) > 100 {
		return nil, errors.New("worker ID must not exceed 100 characters")
	}

	handlers := make(map[string]Handler, len(options.Handlers))
	jobTypes := make([]string, 0, len(options.Handlers))
	for jobType, handler := range options.Handlers {
		if err := validateHandlerType(jobType); err != nil {
			return nil, fmt.Errorf("register handler: %w", err)
		}
		if handler == nil {
			return nil, fmt.Errorf("register handler %s: handler is required", jobType)
		}
		handlers[jobType] = handler
		jobTypes = append(jobTypes, jobType)
	}
	sort.Strings(jobTypes)

	return &Worker{
		logger:         options.Logger,
		queue:          options.Queue,
		handlers:       handlers,
		jobTypes:       jobTypes,
		workerID:       workerID,
		pollInterval:   options.PollInterval,
		jobTimeout:     options.JobTimeout,
		lockTimeout:    options.LockTimeout,
		retryBaseDelay: options.RetryBaseDelay,
		retryMaxDelay:  options.RetryMaxDelay,
	}, nil
}

// Run 执行轮询循环，直到 Context 取消。
//
// Worker 一次只执行一个任务。停止信号会传给当前 Handler，并使用独立短超时提交最终状态；
// 若进程被强制终止，下一实例将通过锁超时恢复任务。
func (worker *Worker) Run(ctx context.Context) error {
	worker.logger.InfoContext(
		ctx,
		"worker started",
		"worker_id", worker.workerID,
		"poll_interval", worker.pollInterval.String(),
		"registered_job_types", worker.jobTypes,
	)

	recoveryInterval := worker.lockTimeout / 2
	if recoveryInterval < worker.pollInterval {
		recoveryInterval = worker.pollInterval
	}
	nextRecovery := time.Time{}

	for {
		if ctx.Err() != nil {
			worker.logger.Info("worker stopped", "worker_id", worker.workerID)
			return nil
		}

		now := time.Now()
		if nextRecovery.IsZero() || !now.Before(nextRecovery) {
			worker.recoverStale(ctx)
			nextRecovery = now.Add(recoveryInterval)
		}

		processed, err := worker.processOne(ctx)
		if err != nil {
			worker.logger.WarnContext(
				ctx,
				"worker poll failed",
				"worker_id", worker.workerID,
				"error", err,
			)
		}
		if processed {
			continue
		}
		if !waitForNextPoll(ctx, worker.pollInterval) {
			worker.logger.Info("worker stopped", "worker_id", worker.workerID)
			return nil
		}
	}
}

func (worker *Worker) processOne(ctx context.Context) (bool, error) {
	job, found, err := worker.queue.Claim(ctx, worker.workerID, worker.jobTypes)
	if err != nil || !found {
		return false, err
	}

	handler := worker.handlers[job.Type]
	handlerContext, cancel := context.WithTimeout(ctx, worker.jobTimeout)
	handlerError := handler.Handle(handlerContext, job)
	cancel()

	finalizeContext, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer finalizeCancel()

	if handlerError == nil {
		if err := worker.queue.MarkSucceeded(finalizeContext, job.ID, worker.workerID); err != nil {
			return true, err
		}
		worker.logger.InfoContext(ctx, "job succeeded", "job_id", job.ID, "job_type", job.Type)
		return true, nil
	}

	errorCode, retryable := classifyHandlerError(handlerError)
	retryDelay := worker.retryDelay(job.Attempts)
	status, err := worker.queue.MarkFailed(
		finalizeContext,
		job.ID,
		worker.workerID,
		errorCode,
		retryable,
		retryDelay,
	)
	if err != nil {
		return true, err
	}
	worker.logger.WarnContext(
		ctx,
		"job execution failed",
		"job_id", job.ID,
		"job_type", job.Type,
		"error_code", errorCode,
		"status", status,
		"attempt", job.Attempts,
		"max_attempts", job.MaxAttempts,
	)
	return true, nil
}

func (worker *Worker) recoverStale(ctx context.Context) {
	result, err := worker.queue.RecoverStale(ctx, worker.lockTimeout, worker.jobTypes)
	if err != nil {
		worker.logger.WarnContext(
			ctx,
			"stale job recovery failed",
			"worker_id", worker.workerID,
			"error", err,
		)
		return
	}
	if result.RetryWaiting > 0 || result.Failed > 0 {
		worker.logger.WarnContext(
			ctx,
			"stale jobs recovered",
			"retry_waiting", result.RetryWaiting,
			"failed", result.Failed,
		)
	}
}

func (worker *Worker) retryDelay(attempt int) time.Duration {
	delay := worker.retryBaseDelay
	for currentAttempt := 1; currentAttempt < attempt; currentAttempt++ {
		if delay >= worker.retryMaxDelay/2 {
			return worker.retryMaxDelay
		}
		delay *= 2
	}
	if delay > worker.retryMaxDelay {
		return worker.retryMaxDelay
	}
	return delay
}

func waitForNextPoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func generateWorkerID() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "worker"
	}
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate worker ID: %w", err)
	}
	workerID := fmt.Sprintf("%s-%d-%s", hostname, os.Getpid(), hex.EncodeToString(randomBytes))
	if len(workerID) > 100 {
		workerID = workerID[len(workerID)-100:]
	}
	return workerID, nil
}
