package bootstrap

import (
	"context"
	"io"
	"strings"

	"agent-chat/internal/application/knowledgeindex"
	domain "agent-chat/internal/domain/knowledge"
	"agent-chat/internal/infrastructure/jobs"
	"agent-chat/internal/infrastructure/model"
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
	if strings.TrimSpace(runtime.config.Models.Embedding.APIKey) != "" {
		embedder, err := model.NewZhipuEmbedder(runtime.config.Models.Embedding)
		if err != nil {
			return err
		}
		indexer, err := knowledgeindex.NewIndexer(
			knowledgepg.NewRepository(runtime.database),
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
		handlers[domain.IndexJobType] = handler
	} else {
		runtime.logger.WarnContext(
			ctx,
			"knowledge indexing disabled",
			"reason", "EMBEDDING_API_KEY is not configured",
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
