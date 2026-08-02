package bootstrap

import (
	"context"
	"io"
	"strings"
	"time"

	agentgraph "agent-chat/internal/agent/graph"
	chatapplication "agent-chat/internal/application/chat"
	"agent-chat/internal/application/knowledgeindex"
	"agent-chat/internal/application/knowledgeretrieve"
	chatdomain "agent-chat/internal/domain/chat"
	knowledgedomain "agent-chat/internal/domain/knowledge"
	crmmock "agent-chat/internal/infrastructure/crm"
	"agent-chat/internal/infrastructure/jobs"
	"agent-chat/internal/infrastructure/model"
	chatpg "agent-chat/internal/infrastructure/persistence/chat"
	knowledgepg "agent-chat/internal/infrastructure/persistence/knowledge"
)

// RunWorker 组装并运行后台 Worker，直到 Context 被取消或启动失败。
func RunWorker(ctx context.Context, output io.Writer) error {
	runtime, err := newRuntime(ctx, output)
	if err != nil {
		return err
	}
	defer runtime.close()

	handlers := make(map[string]jobs.Handler)
	knowledgeRepository := knowledgepg.NewRepository(runtime.database)
	var embedder *model.ZhipuEmbedder
	if strings.TrimSpace(runtime.config.Models.Embedding.APIKey) != "" {
		embedder, err = model.NewZhipuEmbedder(runtime.config.Models.Embedding)
		if err != nil {
			return err
		}
		indexer, err := knowledgeindex.NewIndexer(
			knowledgeRepository,
			embedder,
			knowledgeindex.NewDeterministicChunker(),
		)
		if err != nil {
			return err
		}
		handler, err := jobs.NewKnowledgeIndexHandler(indexer)
		if err != nil {
			return err
		}
		handlers[knowledgedomain.IndexJobType] = handler
	} else {
		runtime.logger.WarnContext(
			ctx,
			"knowledge indexing disabled",
			"reason", "EMBEDDING_API_KEY is not configured",
		)
	}

	if embedder != nil && strings.TrimSpace(runtime.config.Models.Chat.APIKey) != "" {
		chatModel, err := model.NewDeepSeekChatModel(ctx, runtime.config.Models.Chat)
		if err != nil {
			return err
		}
		retrievalService, err := knowledgeretrieve.NewService(
			knowledgeRepository,
			embedder,
		)
		if err != nil {
			return err
		}
		// 演示阶段的 CRM 是内存实现：订阅数据属于外部系统，用表模拟会让人误以为
		// 它是本服务的权威数据。
		subscriptionReader := crmmock.NewReader(time.Now().UTC())
		graphFactory, err := agentgraph.NewFactory(
			retrievalService,
			chatModel,
			agentgraph.DefaultFactoryConfig(),
			agentgraph.WithSubscriptionTool(chatModel, subscriptionReader),
		)
		if err != nil {
			return err
		}
		executor, err := chatapplication.NewExecutor(
			chatpg.NewRepository(runtime.database),
			graphFactory,
			chatapplication.UUIDGenerator{},
			chatapplication.SystemClock{},
			runtime.logger,
		)
		if err != nil {
			return err
		}
		handler, err := jobs.NewAgentRunHandler(executor)
		if err != nil {
			return err
		}
		handlers[chatdomain.AgentRunJobType] = handler
	} else {
		runtime.logger.WarnContext(
			ctx,
			"agent run execution disabled",
			"reason", "LLM_API_KEY and EMBEDDING_API_KEY are both required",
		)
	}

	worker, err := jobs.NewWorker(jobs.WorkerOptions{
		Logger:         runtime.logger,
		Queue:          jobs.NewPostgresQueue(runtime.database),
		Handlers:       handlers,
		WorkerID:       runtime.config.Worker.ID,
		PollInterval:   runtime.config.Worker.PollInterval,
		JobTimeout:     runtime.config.Worker.JobTimeout,
		LockTimeout:    runtime.config.Worker.LockTimeout,
		RetryBaseDelay: runtime.config.Worker.RetryBaseDelay,
		RetryMaxDelay:  runtime.config.Worker.RetryMaxDelay,
	})
	if err != nil {
		return err
	}
	return worker.Run(ctx)
}
