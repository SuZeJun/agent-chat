package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// StatusPending 表示任务等待首次执行。
	StatusPending = "pending"
	// StatusRunning 表示任务已被某个 Worker 租用。
	StatusRunning = "running"
	// StatusRetryWait 表示任务等待下一次重试。
	StatusRetryWait = "retry_wait"
	// StatusSucceeded 表示任务执行成功。
	StatusSucceeded = "succeeded"
	// StatusFailed 表示任务已永久失败或耗尽重试次数。
	StatusFailed = "failed"
)

var (
	// ErrLeaseLost 表示任务已不再由当前 Worker 持有，调用方不得继续提交结果。
	ErrLeaseLost = errors.New("job lease lost")
)

// Job 是 Worker 从持久化队列领取的任务快照。
//
// Attempts 在领取事务中递增，因此 Handler 看到的值包含本次执行。
type Job struct {
	ID             string
	Type           string
	IdempotencyKey string
	Payload        json.RawMessage
	Attempts       int
	MaxAttempts    int
	AvailableAt    time.Time
	LockedAt       time.Time
	LockedBy       string
}

// Validate 校验从持久化层读取的任务是否满足 Worker 执行契约。
func (job Job) Validate() error {
	if strings.TrimSpace(job.ID) == "" {
		return errors.New("job ID must not be blank")
	}
	if strings.TrimSpace(job.Type) == "" {
		return errors.New("job type must not be blank")
	}
	if !json.Valid(job.Payload) {
		return errors.New("job payload must be valid JSON")
	}
	if job.Attempts <= 0 || job.MaxAttempts <= 0 || job.Attempts > job.MaxAttempts {
		return errors.New("job attempts are invalid")
	}
	if job.LockedAt.IsZero() || strings.TrimSpace(job.LockedBy) == "" {
		return errors.New("job lease is incomplete")
	}
	return nil
}

// Handler 执行一种已注册的持久化任务。
//
// Handler 必须以 Job.ID 或 IdempotencyKey 保证副作用幂等，因为进程可能在业务操作成功、
// 状态提交前退出，导致同一任务被锁恢复机制再次投递。Handler 还必须响应 Context 取消，
// 避免执行超过租约恢复时限后与其他 Worker 并发产生副作用。
type Handler interface {
	Handle(context.Context, Job) error
}

// HandlerFunc 允许使用函数注册 Job Handler。
type HandlerFunc func(context.Context, Job) error

// Handle 实现 Handler。
func (handler HandlerFunc) Handle(ctx context.Context, job Job) error {
	return handler(ctx, job)
}

// HandlerError 向 Worker 提供稳定错误码和是否可重试，不暴露底层敏感错误。
type HandlerError struct {
	code      string
	retryable bool
	cause     error
}

// Error 返回可安全写入日志和 jobs.last_error 的稳定错误码。
func (err *HandlerError) Error() string {
	return err.code
}

// Unwrap 仅供进程内错误判断使用；Worker 不会持久化或记录底层 cause。
func (err *HandlerError) Unwrap() error {
	return err.cause
}

// Retryable 将错误标记为可重试，并要求提供稳定、非敏感的错误码。
func Retryable(code string, cause error) error {
	return newHandlerError(code, true, cause)
}

// Permanent 将错误标记为不可重试，并要求提供稳定、非敏感的错误码。
func Permanent(code string, cause error) error {
	return newHandlerError(code, false, cause)
}

func newHandlerError(code string, retryable bool, cause error) error {
	code = strings.TrimSpace(code)
	if code == "" {
		code = "handler_failed"
	}
	if len(code) > 100 {
		code = code[:100]
	}
	return &HandlerError{
		code:      code,
		retryable: retryable,
		cause:     cause,
	}
}

func classifyHandlerError(err error) (code string, retryable bool) {
	var handlerError *HandlerError
	if errors.As(err, &handlerError) {
		return handlerError.code, handlerError.retryable
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "job_timeout", true
	case errors.Is(err, context.Canceled):
		return "worker_interrupted", true
	default:
		return "handler_failed", true
	}
}

func validateHandlerType(jobType string) error {
	if strings.TrimSpace(jobType) == "" {
		return errors.New("job type must not be blank")
	}
	if len(jobType) > 100 {
		return fmt.Errorf("job type must not exceed 100 characters")
	}
	return nil
}
