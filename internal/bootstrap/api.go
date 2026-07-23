package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	httptransport "agent-chat/internal/transport/http"
)

func RunAPI(ctx context.Context, output io.Writer) error {
	runtime, err := newRuntime(ctx, output)
	if err != nil {
		return err
	}
	defer runtime.close()

	router := httptransport.NewRouter(httptransport.RouterOptions{
		Logger:              runtime.logger,
		Database:            runtime.database,
		DatabasePingTimeout: runtime.config.Database.PingTimeout,
		Environment:         runtime.config.App.Environment,
	})
	server := &http.Server{
		Addr:              runtime.config.App.HTTPAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen http: %w", err)
	}

	errorChannel := make(chan error, 1)
	go func() {
		runtime.logger.Info("api started", "address", runtime.config.App.HTTPAddress)
		errorChannel <- server.Serve(listener)
	}()

	select {
	case err := <-errorChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), runtime.config.App.ShutdownTimeout)
		defer cancel()
		runtime.logger.Info("api shutting down")
		shutdownErr := server.Shutdown(shutdownContext)
		if shutdownErr != nil {
			_ = server.Close()
		}
		serveErr := <-errorChannel
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		if shutdownErr != nil || serveErr != nil {
			return errors.Join(
				wrapError("shutdown http server", shutdownErr),
				wrapError("serve http", serveErr),
			)
		}
		runtime.logger.Info("api stopped")
		return nil
	}
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
