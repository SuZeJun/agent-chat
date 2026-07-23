package httptransport

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

const requestIDHeader = "X-Request-ID"

func requestContext(logger *slog.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		requestID := newRequestID()
		ctx.Set("request_id", requestID)
		ctx.Header(requestIDHeader, requestID)

		startedAt := time.Now()
		ctx.Next()
		logger.InfoContext(ctx.Request.Context(), "http request",
			"request_id", requestID,
			"method", ctx.Request.Method,
			"path", ctx.Request.URL.Path,
			"status", ctx.Writer.Status(),
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"client_ip", ctx.ClientIP(),
		)
	}
}

func newRequestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "request-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(value)
}

func recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(ctx.Request.Context(), "http panic recovered",
					"request_id", ctx.GetString("request_id"),
					"panic_type", fmt.Sprintf("%T", recovered),
					"stack", string(debug.Stack()),
				)
				ctx.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code":    "internal_error",
					"message": "internal server error",
				})
			}
		}()
		ctx.Next()
	}
}
