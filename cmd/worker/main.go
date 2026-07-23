package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"agent-chat/internal/bootstrap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bootstrap.RunWorker(ctx, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "worker failed: %v\n", err)
		os.Exit(1)
	}
}
