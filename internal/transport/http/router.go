package httptransport

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type DatabaseHealth interface {
	Ping(context.Context) error
}

type RouterOptions struct {
	Logger              *slog.Logger
	Database            DatabaseHealth
	DatabasePingTimeout time.Duration
	Environment         string
}

func NewRouter(options RouterOptions) *gin.Engine {
	if options.Environment != "development" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(requestContext(options.Logger), recovery(options.Logger))

	router.GET("/healthz", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(ctx *gin.Context) {
		pingContext, cancel := context.WithTimeout(ctx.Request.Context(), options.DatabasePingTimeout)
		defer cancel()
		if err := options.Database.Ping(pingContext); err != nil {
			options.Logger.WarnContext(ctx.Request.Context(), "readiness check failed",
				"request_id", ctx.GetString("request_id"),
				"error", err,
			)
			ctx.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unavailable",
				"checks": gin.H{"database": "unavailable"},
			})
			return
		}
		ctx.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"checks": gin.H{"database": "ok"},
		})
	})
	return router
}
