package httptransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeDatabaseHealth struct {
	err error
}

func (health fakeDatabaseHealth) Ping(context.Context) error {
	return health.err
}

func TestHealthz(t *testing.T) {
	router := newTestRouter(fakeDatabaseHealth{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	if response.Header().Get(requestIDHeader) == "" {
		t.Fatal("expected request ID response header")
	}
}

func TestRequestIDDoesNotTrustClientHeader(t *testing.T) {
	router := newTestRouter(fakeDatabaseHealth{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(requestIDHeader, "client-controlled-request-id")

	router.ServeHTTP(response, request)

	requestID := response.Header().Get(requestIDHeader)
	if requestID == "" || requestID == "client-controlled-request-id" {
		t.Fatalf("unexpected request ID: %q", requestID)
	}
}

func TestReadyzReportsDatabaseFailure(t *testing.T) {
	router := newTestRouter(fakeDatabaseHealth{err: errors.New("database unavailable")})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}

func TestRecoveryDoesNotLogPanicValue(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	router := gin.New()
	router.Use(requestContext(logger), recovery(logger))
	router.GET("/panic", func(*gin.Context) {
		panic("secret-panic-value")
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	logOutput := output.String()
	if strings.Contains(logOutput, "secret-panic-value") {
		t.Fatalf("panic value leaked into logs: %s", logOutput)
	}
	if strings.Count(logOutput, "http panic recovered") != 1 {
		t.Fatalf("expected one recovery log entry: %s", logOutput)
	}
}

func newTestRouter(database DatabaseHealth) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(RouterOptions{
		Logger:              logger,
		Database:            database,
		DatabasePingTimeout: time.Second,
		Environment:         "test",
	})
}
