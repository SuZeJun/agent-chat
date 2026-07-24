package graph

import (
	"context"
	"errors"
	"math"

	"agent-chat/internal/agent/retrieval"
)

const (
	defaultRetrieverTopK           = 5
	defaultRetrieverScoreThreshold = 0.0
)

// Runner 是 Application 执行 RAG Graph 所需的最小接口。
type Runner interface {
	Run(context.Context, Input) (Output, error)
}

// FactoryConfig 固定每个知识库 Runtime 共用的检索和 Graph 参数。
type FactoryConfig struct {
	RetrieverTopK           int
	RetrieverScoreThreshold float64
	Graph                   Config
}

// DefaultFactoryConfig 返回允许 Answerability Gate 观察低分证据的默认配置。
func DefaultFactoryConfig() FactoryConfig {
	return FactoryConfig{
		RetrieverTopK:           defaultRetrieverTopK,
		RetrieverScoreThreshold: defaultRetrieverScoreThreshold,
		Graph:                   DefaultConfig(),
	}
}

// Factory 为会话绑定的知识库创建隔离的 RAG Runtime。
type Factory struct {
	retrievalService retrieval.Service
	chatModel        ChatModel
	config           FactoryConfig
}

// NewFactory 创建 RAG Runtime Factory。
func NewFactory(
	retrievalService retrieval.Service,
	chatModel ChatModel,
	config FactoryConfig,
) (*Factory, error) {
	if retrievalService == nil {
		return nil, errors.New("RAG factory retrieval service is required")
	}
	if chatModel == nil {
		return nil, errors.New("RAG factory chat model is required")
	}
	if config.RetrieverTopK <= 0 || config.RetrieverTopK > 100 {
		return nil, errors.New("RAG factory TopK must be between 1 and 100")
	}
	if config.RetrieverScoreThreshold < -1 ||
		config.RetrieverScoreThreshold > 1 ||
		math.IsNaN(config.RetrieverScoreThreshold) ||
		math.IsInf(config.RetrieverScoreThreshold, 0) {
		return nil, errors.New("RAG factory score threshold is invalid")
	}
	if err := config.Graph.validate(); err != nil {
		return nil, err
	}
	return &Factory{
		retrievalService: retrievalService,
		chatModel:        chatModel,
		config:           config,
	}, nil
}

// Build 创建绑定单个服务端知识库 ID 的 Eino Retriever 和 RAG Graph。
func (factory *Factory) Build(
	ctx context.Context,
	knowledgeBaseID string,
) (Runner, error) {
	retriever, err := retrieval.NewKnowledgeRetriever(
		factory.retrievalService,
		retrieval.Config{
			KnowledgeBaseID:       knowledgeBaseID,
			DefaultTopK:           factory.config.RetrieverTopK,
			DefaultScoreThreshold: factory.config.RetrieverScoreThreshold,
		},
	)
	if err != nil {
		return nil, err
	}
	return NewRuntime(ctx, retriever, factory.chatModel, factory.config.Graph)
}
