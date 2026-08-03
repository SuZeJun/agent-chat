package httptransport

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// DatabaseHealth 定义 HTTP 就绪检查所需的最小数据库能力。
type DatabaseHealth interface {
	Ping(context.Context) error
}

// RouterOptions 定义创建 HTTP Router 所需的依赖和运行参数。
type RouterOptions struct {
	Logger              *slog.Logger
	Database            DatabaseHealth
	DatabasePingTimeout time.Duration
	Environment         string
	KnowledgeBase       KnowledgeBaseCreator
	FAQImport           FAQImportService
	Conversation        ConversationCreator
	Message             MessageSender
	RunEvents           RunEventReader
	RunTrace            RunTraceReader
	TicketApproval      TicketApprover
}

// NewRouter 创建包含中间件、存活检查和就绪检查的 Gin Engine。
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
	registerKnowledgeRoutes(router, options.KnowledgeBase, options.FAQImport)
	registerChatRoutes(
		router,
		options.Conversation,
		options.Message,
		options.RunEvents,
	)
	registerRunTraceRoute(router, options.RunTrace)
	registerTicketRoutes(router, options.TicketApproval)
	return router
}
