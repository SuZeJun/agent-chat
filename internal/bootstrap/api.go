package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	chatapplication "agent-chat/internal/application/chat"
	"agent-chat/internal/application/knowledgebase"
	"agent-chat/internal/application/knowledgedocument"
	"agent-chat/internal/application/knowledgeimport"
	ticketapp "agent-chat/internal/application/ticket"
	knowledgedomain "agent-chat/internal/domain/knowledge"
	chatpg "agent-chat/internal/infrastructure/persistence/chat"
	knowledgepg "agent-chat/internal/infrastructure/persistence/knowledge"
	ticketpg "agent-chat/internal/infrastructure/persistence/ticket"
	httptransport "agent-chat/internal/transport/http"
)

// RunAPI 组装并运行 HTTP API，直到服务失败或 Context 被取消。
func RunAPI(ctx context.Context, output io.Writer) error {
	runtime, err := newRuntime(ctx, output)
	if err != nil {
		return err
	}
	defer runtime.close()

	knowledgeRepository := knowledgepg.NewRepository(runtime.database)
	idGenerator := knowledgeimport.UUIDGenerator{}
	knowledgeBaseService, err := knowledgebase.NewService(
		knowledgeRepository,
		idGenerator,
	)
	if err != nil {
		return err
	}
	importService, err := knowledgeimport.NewService(
		knowledgeRepository,
		knowledgedomain.EmbeddingIdentity{
			Provider:   "zhipu",
			Model:      runtime.config.Models.Embedding.Model,
			Dimensions: runtime.config.Models.Embedding.Dimensions,
		},
		idGenerator,
		knowledgeimport.SystemClock{},
	)
	if err != nil {
		return err
	}
	markdownService, err := knowledgedocument.NewService(
		knowledgeRepository,
		knowledgedomain.EmbeddingIdentity{
			Provider:   "zhipu",
			Model:      runtime.config.Models.Embedding.Model,
			Dimensions: runtime.config.Models.Embedding.Dimensions,
		},
		idGenerator,
	)
	if err != nil {
		return err
	}
	chatRepository := chatpg.NewRepository(runtime.database)
	chatIDGenerator := chatapplication.UUIDGenerator{}
	chatClock := chatapplication.SystemClock{}
	conversationService, err := chatapplication.NewConversationService(
		chatRepository,
		chatIDGenerator,
		chatClock,
	)
	if err != nil {
		return err
	}
	messageService, err := chatapplication.NewService(
		chatRepository,
		chatIDGenerator,
		chatClock,
	)
	if err != nil {
		return err
	}
	historyService, err := chatapplication.NewHistoryService(chatRepository)
	if err != nil {
		return err
	}
	eventService, err := chatapplication.NewEventService(chatRepository)
	if err != nil {
		return err
	}
	traceService, err := chatapplication.NewTraceService(chatRepository)
	if err != nil {
		return err
	}
	handoffService, err := chatapplication.NewHandoffService(
		chatRepository,
		chatIDGenerator,
		chatClock,
	)
	if err != nil {
		return err
	}
	ticketService, err := ticketapp.NewService(
		ticketpg.NewRepository(runtime.database),
		chatapplication.UUIDGenerator{},
		chatapplication.SystemClock{},
	)
	if err != nil {
		return err
	}
	router := httptransport.NewRouter(httptransport.RouterOptions{
		Logger:              runtime.logger,
		Database:            runtime.database,
		DatabasePingTimeout: runtime.config.Database.PingTimeout,
		Environment:         runtime.config.App.Environment,
		KnowledgeBase:       knowledgeBaseService,
		FAQImport:           importService,
		MarkdownDocument:    markdownService,
		Conversation:        conversationService,
		Message:             messageService,
		MessageHistory:      historyService,
		RunEvents:           eventService,
		RunTrace:            traceService,
		TicketApproval:      ticketService,
		Handoff:             handoffService,
	})
	server := &http.Server{
		Addr:              runtime.config.App.HTTPAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	// 先同步创建 listener，避免 Context 已取消而 Serve 尚未启动时出现关闭竞态。
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
		// Shutdown 只负责发起关闭，仍需读取 Serve 的最终结果以保留真实错误。
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
