package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
)

type failureCall struct {
	jobID      string
	errorCode  string
	retryable  bool
	retryDelay time.Duration
}

type fakeQueue struct {
	mu            sync.Mutex
	jobs          []Job
	claimedTypes  []string
	succeeded     []string
	failures      []failureCall
	recoverCalls  int
	onSucceeded   func()
	onFailed      func()
	failureStatus string
}

func (queue *fakeQueue) Claim(
	_ context.Context,
	_ string,
	jobTypes []string,
) (Job, bool, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.claimedTypes = append([]string(nil), jobTypes...)
	if len(queue.jobs) == 0 {
		return Job{}, false, nil
	}
	job := queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	return job, true, nil
}

func (queue *fakeQueue) MarkSucceeded(_ context.Context, jobID string, _ string) error {
	queue.mu.Lock()
	queue.succeeded = append(queue.succeeded, jobID)
	callback := queue.onSucceeded
	queue.mu.Unlock()
	if callback != nil {
		callback()
	}
	return nil
}

func (queue *fakeQueue) MarkFailed(
	_ context.Context,
	jobID string,
	_ string,
	errorCode string,
	retryable bool,
	retryDelay time.Duration,
) (string, error) {
	queue.mu.Lock()
	queue.failures = append(queue.failures, failureCall{
		jobID:      jobID,
		errorCode:  errorCode,
		retryable:  retryable,
		retryDelay: retryDelay,
	})
	callback := queue.onFailed
	status := queue.failureStatus
	queue.mu.Unlock()
	if callback != nil {
		callback()
	}
	if status == "" {
		status = StatusRetryWait
	}
	return status, nil
}

func (queue *fakeQueue) RecoverStale(
	context.Context,
	time.Duration,
	[]string,
) (RecoveryResult, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.recoverCalls++
	return RecoveryResult{}, nil
}

func TestWorkerExecutesRegisteredJobAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &fakeQueue{
		jobs:        []Job{testJob("job-1", "knowledge.index", 1)},
		onSucceeded: cancel,
	}
	var handled []string
	worker := newTestWorker(t, queue, map[string]Handler{
		"knowledge.index": HandlerFunc(func(_ context.Context, job Job) error {
			handled = append(handled, job.ID)
			return nil
		}),
	})

	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !slices.Equal(handled, []string{"job-1"}) {
		t.Fatalf("unexpected handled jobs: %#v", handled)
	}
	if !slices.Equal(queue.succeeded, []string{"job-1"}) {
		t.Fatalf("unexpected succeeded jobs: %#v", queue.succeeded)
	}
	if !slices.Equal(queue.claimedTypes, []string{"knowledge.index"}) {
		t.Fatalf("unexpected claimed types: %#v", queue.claimedTypes)
	}
}

func TestWorkerRetriesClassifiedFailureThenSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &fakeQueue{
		jobs: []Job{
			testJob("job-1", "knowledge.index", 1),
			testJob("job-1", "knowledge.index", 2),
		},
		onSucceeded: cancel,
	}
	calls := 0
	worker := newTestWorker(t, queue, map[string]Handler{
		"knowledge.index": HandlerFunc(func(context.Context, Job) error {
			calls++
			if calls == 1 {
				return Retryable("embedding_unavailable", errors.New("sensitive provider detail"))
			}
			return nil
		}),
	})

	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(queue.failures) != 1 {
		t.Fatalf("unexpected failures: %#v", queue.failures)
	}
	failure := queue.failures[0]
	if failure.errorCode != "embedding_unavailable" || !failure.retryable {
		t.Fatalf("unexpected failure classification: %#v", failure)
	}
	if failure.retryDelay != 10*time.Millisecond {
		t.Fatalf("unexpected first retry delay: %s", failure.retryDelay)
	}
}

func TestWorkerDoesNotRetryPermanentFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &fakeQueue{
		jobs:          []Job{testJob("job-1", "knowledge.index", 1)},
		onFailed:      cancel,
		failureStatus: StatusFailed,
	}
	worker := newTestWorker(t, queue, map[string]Handler{
		"knowledge.index": HandlerFunc(func(context.Context, Job) error {
			return Permanent("invalid_payload", errors.New("raw payload detail"))
		}),
	})

	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(queue.failures) != 1 || queue.failures[0].retryable {
		t.Fatalf("unexpected failures: %#v", queue.failures)
	}
	if queue.failures[0].errorCode != "invalid_payload" {
		t.Fatalf("unexpected error code: %q", queue.failures[0].errorCode)
	}
}

func TestWorkerCancellationReleasesCurrentJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	queue := &fakeQueue{
		jobs:     []Job{testJob("job-1", "knowledge.index", 1)},
		onFailed: func() {},
	}
	worker := newTestWorker(t, queue, map[string]Handler{
		"knowledge.index": HandlerFunc(func(ctx context.Context, _ Job) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}),
	})

	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx)
	}()
	<-started
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if len(queue.failures) != 1 {
		t.Fatalf("unexpected failures: %#v", queue.failures)
	}
	if queue.failures[0].errorCode != "worker_interrupted" || !queue.failures[0].retryable {
		t.Fatalf("unexpected cancellation classification: %#v", queue.failures[0])
	}
}

func TestWorkerWithoutHandlersDoesNotClaimJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &fakeQueue{}
	worker := newTestWorker(t, queue, nil)

	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(queue.claimedTypes) != 0 {
		t.Fatalf("worker without handlers attempted claim: %#v", queue.claimedTypes)
	}
}

func TestWorkerRetryDelayIsExponentiallyBounded(t *testing.T) {
	worker := newTestWorker(t, &fakeQueue{}, nil)
	tests := map[int]time.Duration{
		1: 10 * time.Millisecond,
		2: 20 * time.Millisecond,
		3: 40 * time.Millisecond,
		4: 40 * time.Millisecond,
		9: 40 * time.Millisecond,
	}
	for attempt, expected := range tests {
		if actual := worker.retryDelay(attempt); actual != expected {
			t.Fatalf("attempt %d: expected %s, got %s", attempt, expected, actual)
		}
	}
}

func newTestWorker(t *testing.T, queue Queue, handlers map[string]Handler) *Worker {
	t.Helper()
	worker, err := NewWorker(WorkerOptions{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Queue:          queue,
		Handlers:       handlers,
		WorkerID:       "worker-test",
		PollInterval:   5 * time.Millisecond,
		JobTimeout:     time.Second,
		LockTimeout:    2 * time.Second,
		RetryBaseDelay: 10 * time.Millisecond,
		RetryMaxDelay:  40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWorker returned error: %v", err)
	}
	return worker
}

func testJob(jobID string, jobType string, attempts int) Job {
	return Job{
		ID:          jobID,
		Type:        jobType,
		Payload:     []byte(`{}`),
		Attempts:    attempts,
		MaxAttempts: 5,
		AvailableAt: time.Now(),
		LockedAt:    time.Now(),
		LockedBy:    "worker-test",
	}
}
