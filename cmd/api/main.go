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

	if err := bootstrap.RunAPI(ctx, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "api failed: %v\n", err)
		os.Exit(1)
	}
}
