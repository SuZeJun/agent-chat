package jobs

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type countingHealth struct {
	count atomic.Int32
}

func (health *countingHealth) Ping(context.Context) error {
	health.count.Add(1)
	return nil
}

func TestWorkerStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	database := &countingHealth{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(logger, database, time.Millisecond, time.Second)

	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for database.count.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}
