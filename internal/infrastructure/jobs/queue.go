package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queue 定义 Worker 使用持久化任务租约所需的最小能力。
type Queue interface {
	Claim(context.Context, string, []string) (Job, bool, error)
	MarkSucceeded(context.Context, string, string) error
	MarkFailed(context.Context, string, string, string, bool, time.Duration) (string, error)
	RecoverStale(context.Context, time.Duration, []string) (RecoveryResult, error)
}

// RecoveryResult 汇总一次锁超时恢复产生的状态转换。
type RecoveryResult struct {
	RetryWaiting int
	Failed       int
}

// PostgresQueue 使用 PostgreSQL 行锁实现跨进程安全的任务领取和状态提交。
type PostgresQueue struct {
	database *pgxpool.Pool
}

// NewPostgresQueue 创建 PostgreSQL 持久化任务队列。
func NewPostgresQueue(database *pgxpool.Pool) *PostgresQueue {
	return &PostgresQueue{database: database}
}

// Claim 原子领取一个到期任务，并在同一语句中递增尝试次数和写入租约。
//
// SKIP LOCKED 允许多个 Worker 并发轮询，而不会等待或重复领取同一行。
func (queue *PostgresQueue) Claim(
	ctx context.Context,
	workerID string,
	jobTypes []string,
) (Job, bool, error) {
	if len(jobTypes) == 0 {
		return Job{}, false, nil
	}

	var job Job
	var payload []byte
	err := queue.database.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id
			FROM jobs
			WHERE status IN ('pending', 'retry_wait')
			  AND attempts < max_attempts
			  AND available_at <= now()
			  AND job_type = ANY($2::varchar[])
			ORDER BY available_at, created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE jobs AS target
		SET status = 'running',
			attempts = target.attempts + 1,
			locked_at = now(),
			locked_by = $1,
			updated_at = now()
		FROM candidate
		WHERE target.id = candidate.id
		RETURNING
			target.id,
			target.job_type,
			target.idempotency_key,
			target.payload,
			target.attempts,
			target.max_attempts,
			target.available_at,
			target.locked_at,
			target.locked_by
	`, workerID, jobTypes).Scan(
		&job.ID,
		&job.Type,
		&job.IdempotencyKey,
		&payload,
		&job.Attempts,
		&job.MaxAttempts,
		&job.AvailableAt,
		&job.LockedAt,
		&job.LockedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, mapQueueError("claim job", err)
	}
	job.Payload = payload
	if err := job.Validate(); err != nil {
		return Job{}, false, fmt.Errorf("claim job: invalid persisted job: %w", err)
	}
	return job, true, nil
}

// MarkSucceeded 仅允许当前租约持有者把运行中的任务标记为成功。
func (queue *PostgresQueue) MarkSucceeded(
	ctx context.Context,
	jobID string,
	workerID string,
) error {
	commandTag, err := queue.database.Exec(ctx, `
		UPDATE jobs
		SET status = 'succeeded',
			locked_at = NULL,
			locked_by = '',
			last_error = '',
			updated_at = now()
		WHERE id = $1
		  AND status = 'running'
		  AND locked_by = $2
	`, jobID, workerID)
	if err != nil {
		return mapQueueError("mark job succeeded", err)
	}
	if commandTag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// MarkFailed 根据错误可重试性和剩余次数，将任务转为 retry_wait 或 failed。
//
// last_error 只保存 Worker 分类后的稳定错误码，不保存可能包含密钥或业务内容的原始错误。
func (queue *PostgresQueue) MarkFailed(
	ctx context.Context,
	jobID string,
	workerID string,
	errorCode string,
	retryable bool,
	retryDelay time.Duration,
) (string, error) {
	delayMilliseconds := max(retryDelay.Milliseconds(), int64(0))
	var status string
	err := queue.database.QueryRow(ctx, `
		UPDATE jobs
		SET status = CASE
				WHEN $4 AND attempts < max_attempts THEN 'retry_wait'
				ELSE 'failed'
			END,
			available_at = CASE
				WHEN $4 AND attempts < max_attempts
					THEN now() + ($5::bigint * interval '1 millisecond')
				ELSE available_at
			END,
			locked_at = NULL,
			locked_by = '',
			last_error = $3,
			updated_at = now()
		WHERE id = $1
		  AND status = 'running'
		  AND locked_by = $2
		RETURNING status
	`, jobID, workerID, errorCode, retryable, delayMilliseconds).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLeaseLost
	}
	if err != nil {
		return "", mapQueueError("mark job failed", err)
	}
	return status, nil
}

// RecoverStale 释放超过锁定时限的任务；已耗尽次数的任务直接进入 failed。
//
// 该操作不依赖原 Worker 存活，保证进程崩溃后任务最终可恢复。
func (queue *PostgresQueue) RecoverStale(
	ctx context.Context,
	lockTimeout time.Duration,
	jobTypes []string,
) (RecoveryResult, error) {
	if len(jobTypes) == 0 {
		return RecoveryResult{}, nil
	}
	timeoutMilliseconds := max(lockTimeout.Milliseconds(), int64(1))
	rows, err := queue.database.Query(ctx, `
		UPDATE jobs
		SET status = CASE
				WHEN attempts < max_attempts THEN 'retry_wait'
				ELSE 'failed'
			END,
			available_at = CASE
				WHEN attempts < max_attempts THEN now()
				ELSE available_at
			END,
			locked_at = NULL,
			locked_by = '',
			last_error = 'worker_lock_expired',
			updated_at = now()
		WHERE status = 'running'
		  AND locked_at < now() - ($1::bigint * interval '1 millisecond')
		  AND job_type = ANY($2::varchar[])
		RETURNING status
	`, timeoutMilliseconds, jobTypes)
	if err != nil {
		return RecoveryResult{}, mapQueueError("recover stale jobs", err)
	}
	defer rows.Close()

	var result RecoveryResult
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return RecoveryResult{}, mapQueueError("recover stale jobs", err)
		}
		switch status {
		case StatusRetryWait:
			result.RetryWaiting++
		case StatusFailed:
			result.Failed++
		}
	}
	if err := rows.Err(); err != nil {
		return RecoveryResult{}, mapQueueError("recover stale jobs", err)
	}
	return result, nil
}

func mapQueueError(operation string, err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", operation, err)
	default:
		return fmt.Errorf("%s: database operation failed", operation)
	}
}
