package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-chat/internal/infrastructure/persistence"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresQueueLifecycleAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openJobTestDatabase(t, ctx, databaseURL)
	defer pool.Close()
	queue := NewPostgresQueue(pool)

	insertTestJob(t, ctx, pool, "job-concurrent", "knowledge.index", 5)
	var claimed atomic.Int32
	var claimedJob Job
	var claimedMutex sync.Mutex
	var waitGroup sync.WaitGroup
	for workerIndex := range 12 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			job, found, err := queue.Claim(
				ctx,
				fmt.Sprintf("worker-%d", workerIndex),
				[]string{"knowledge.index"},
			)
			if err != nil {
				t.Errorf("claim concurrent job: %v", err)
				return
			}
			if found {
				claimed.Add(1)
				claimedMutex.Lock()
				claimedJob = job
				claimedMutex.Unlock()
			}
		}()
	}
	waitGroup.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("expected exactly one claim, got %d", claimed.Load())
	}
	if claimedJob.Attempts != 1 {
		t.Fatalf("unexpected attempts after claim: %d", claimedJob.Attempts)
	}
	if err := queue.MarkSucceeded(ctx, claimedJob.ID, "other-worker"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected lease loss for wrong worker, got %v", err)
	}
	if err := queue.MarkSucceeded(ctx, claimedJob.ID, claimedJob.LockedBy); err != nil {
		t.Fatalf("mark concurrent job succeeded: %v", err)
	}
	assertJobState(t, ctx, pool, claimedJob.ID, StatusSucceeded, 1, "")

	insertTestJob(t, ctx, pool, "job-unsupported", "agent.run", 5)
	if _, found, err := queue.Claim(ctx, "worker-filter", []string{"knowledge.index"}); err != nil {
		t.Fatalf("claim filtered job: %v", err)
	} else if found {
		t.Fatal("claimed an unregistered job type")
	}

	insertTestJob(t, ctx, pool, "job-retry", "knowledge.index", 2)
	retryJob, found, err := queue.Claim(ctx, "worker-retry", []string{"knowledge.index"})
	if err != nil || !found {
		t.Fatalf("claim retry job: found=%t err=%v", found, err)
	}
	status, err := queue.MarkFailed(
		ctx,
		retryJob.ID,
		retryJob.LockedBy,
		"temporary_failure",
		true,
		200*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	if status != StatusRetryWait {
		t.Fatalf("unexpected retry status: %s", status)
	}
	assertJobState(t, ctx, pool, retryJob.ID, StatusRetryWait, 1, "temporary_failure")
	if _, found, err := queue.Claim(ctx, "worker-retry", []string{"knowledge.index"}); err != nil {
		t.Fatalf("claim delayed retry: %v", err)
	} else if found {
		t.Fatal("claimed retry before available_at")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs
		SET available_at = now()
		WHERE id = $1
	`, retryJob.ID); err != nil {
		t.Fatalf("make retry available: %v", err)
	}
	retryJob, found, err = queue.Claim(ctx, "worker-retry", []string{"knowledge.index"})
	if err != nil || !found {
		t.Fatalf("claim final attempt: found=%t err=%v", found, err)
	}
	status, err = queue.MarkFailed(
		ctx,
		retryJob.ID,
		retryJob.LockedBy,
		"temporary_failure",
		true,
		time.Second,
	)
	if err != nil {
		t.Fatalf("mark exhausted retry: %v", err)
	}
	if status != StatusFailed {
		t.Fatalf("unexpected exhausted status: %s", status)
	}
	assertJobState(t, ctx, pool, retryJob.ID, StatusFailed, 2, "temporary_failure")

	insertRunningTestJob(t, ctx, pool, "job-stale-retry", 1, 3, 10*time.Minute)
	insertRunningTestJob(t, ctx, pool, "job-stale-failed", 2, 2, 10*time.Minute)
	insertRunningTestJob(t, ctx, pool, "job-fresh", 1, 3, 10*time.Second)
	insertRunningTestJobWithType(t, ctx, pool, "job-stale-unsupported", "agent.run", 1, 3, 10*time.Minute)
	recovery, err := queue.RecoverStale(ctx, time.Minute, []string{"knowledge.index"})
	if err != nil {
		t.Fatalf("recover stale jobs: %v", err)
	}
	if recovery.RetryWaiting != 1 || recovery.Failed != 1 {
		t.Fatalf("unexpected recovery result: %#v", recovery)
	}
	assertJobState(t, ctx, pool, "job-stale-retry", StatusRetryWait, 1, "worker_lock_expired")
	assertJobState(t, ctx, pool, "job-stale-failed", StatusFailed, 2, "worker_lock_expired")
	assertJobState(t, ctx, pool, "job-fresh", StatusRunning, 1, "")
	assertJobState(t, ctx, pool, "job-stale-unsupported", StatusRunning, 1, "")
}

func openJobTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	schemaName := fmt.Sprintf("job_queue_test_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(cleanupContext, "DROP SCHEMA "+schemaIdentifier+" CASCADE")
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := persistence.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

func insertTestJob(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID string,
	jobType string,
	maxAttempts int,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (id, job_type, payload, status, max_attempts)
		VALUES ($1, $2, '{"test":true}', 'pending', $3)
	`, jobID, jobType, maxAttempts); err != nil {
		t.Fatalf("insert job %s: %v", jobID, err)
	}
}

func insertRunningTestJob(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID string,
	attempts int,
	maxAttempts int,
	lockedAgo time.Duration,
) {
	t.Helper()
	insertRunningTestJobWithType(
		t,
		ctx,
		pool,
		jobID,
		"knowledge.index",
		attempts,
		maxAttempts,
		lockedAgo,
	)
}

func insertRunningTestJobWithType(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID string,
	jobType string,
	attempts int,
	maxAttempts int,
	lockedAgo time.Duration,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (
			id,
			job_type,
			payload,
			status,
			attempts,
			max_attempts,
			locked_at,
			locked_by
		)
		VALUES (
			$1,
			$2,
			'{"test":true}',
			'running',
			$3,
			$4,
			now() - ($5::bigint * interval '1 millisecond'),
			'crashed-worker'
		)
	`, jobID, jobType, attempts, maxAttempts, lockedAgo.Milliseconds()); err != nil {
		t.Fatalf("insert running job %s: %v", jobID, err)
	}
}

func assertJobState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID string,
	expectedStatus string,
	expectedAttempts int,
	expectedError string,
) {
	t.Helper()

	var status string
	var attempts int
	var lockedAt *time.Time
	var lockedBy string
	var lastError string
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts, locked_at, locked_by, last_error
		FROM jobs
		WHERE id = $1
	`, jobID).Scan(&status, &attempts, &lockedAt, &lockedBy, &lastError); err != nil {
		t.Fatalf("load job %s: %v", jobID, err)
	}
	if status != expectedStatus ||
		attempts != expectedAttempts ||
		lockedAt != nil && expectedStatus != StatusRunning ||
		lockedBy != "" && expectedStatus != StatusRunning ||
		lastError != expectedError {
		t.Fatalf(
			"unexpected job %s state: status=%s attempts=%d locked_at=%v locked_by=%q last_error=%q",
			jobID,
			status,
			attempts,
			lockedAt,
			lockedBy,
			lastError,
		)
	}
}
