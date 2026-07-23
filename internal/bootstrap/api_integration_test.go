package bootstrap

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

type startupWriter struct {
	once    sync.Once
	started chan struct{}
}

func (writer *startupWriter) Write(value []byte) (int, error) {
	if bytes.Contains(value, []byte("api started")) {
		writer.once.Do(func() {
			close(writer.started)
		})
	}
	return len(value), nil
}

func TestRunAPIShutsDownCleanly(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	t.Setenv("APP_ENV", "test")
	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("SHUTDOWN_TIMEOUT", "5s")

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	output := &startupWriter{started: make(chan struct{})}
	go func() {
		result <- RunAPI(ctx, output)
	}()

	select {
	case <-output.started:
	case err := <-result:
		t.Fatalf("RunAPI exited before startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("RunAPI did not start")
	}
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunAPI returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunAPI did not shut down")
	}
}
