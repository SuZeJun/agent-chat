package graph

import (
	"context"
	"errors"
	"math"

	"agent-chat/internal/agent/retrieval"
	agenttool "agent-chat/internal/agent/tool"
	crm "agent-chat/internal/domain/crm"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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

// ToolCallingModel 是可绑定工具声明的模型。
//
// 与 ChatModel 分开声明：绑定工具会返回新实例，而 Graph 只需要绑定完成后的
// planner。未提供该依赖时 Factory 构建纯知识 Runtime。
type ToolCallingModel interface {
	WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error)
}

// Factory 为会话绑定的知识库创建隔离的 RAG Runtime。
type Factory struct {
	retrievalService   retrieval.Service
	chatModel          ChatModel
	toolCallingModel   ToolCallingModel
	subscriptionReader crm.SubscriptionReader
	config             FactoryConfig
}

// WithSubscriptionTool 启用订阅查询工具。
//
// 两个依赖缺一不可：没有可绑定工具的模型就无人选择工具，没有 CRM 就无工具可用。
func WithSubscriptionTool(
	model ToolCallingModel,
	reader crm.SubscriptionReader,
) FactoryOption {
	return func(factory *Factory) {
		if model == nil || reader == nil {
			return
		}
		factory.toolCallingModel = model
		factory.subscriptionReader = reader
	}
}

// FactoryOption 配置 Factory 的可选能力。
type FactoryOption func(*Factory)

// NewFactory 创建 RAG Runtime Factory。
func NewFactory(
	retrievalService retrieval.Service,
	chatModel ChatModel,
	config FactoryConfig,
	options ...FactoryOption,
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
	factory := &Factory{
		retrievalService: retrievalService,
		chatModel:        chatModel,
		config:           config,
	}
	for _, option := range options {
		option(factory)
	}
	return factory, nil
}

// Build 创建绑定单个知识库和单个客户的 Eino Retriever、工具集与 RAG Graph。
//
// 知识库 ID 与客户 ID 都由服务端从会话推导后传入；Runtime 内的检索器和工具都在
// 此处完成作用域绑定，模型无法在运行期改变查询目标。
func (factory *Factory) Build(
	ctx context.Context,
	knowledgeBaseID string,
	customerID string,
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

	options, err := factory.toolOptions(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return NewRuntime(ctx, retriever, factory.chatModel, factory.config.Graph, options...)
}

// toolOptions 为当前客户构建工具注册表并绑定到模型。
//
// 每次 Build 重新构建：工具的授权作用域是每个会话独有的，跨会话复用注册表会让
// 一个客户的工具实例服务于另一个客户。
func (factory *Factory) toolOptions(
	ctx context.Context,
	customerID string,
) ([]RuntimeOption, error) {
	if factory.toolCallingModel == nil || factory.subscriptionReader == nil {
		return nil, nil
	}

	registry, err := newToolRegistry(factory.subscriptionReader, customerID)
	if err != nil {
		return nil, err
	}
	infos, err := registry.Infos(ctx)
	if err != nil {
		return nil, err
	}
	planner, err := factory.toolCallingModel.WithTools(infos)
	if err != nil {
		return nil, err
	}
	return []RuntimeOption{WithTools(planner, registry, customerID)}, nil
}

// newToolRegistry 是生产 Runtime 构建工具白名单的唯一入口。
//
// 草稿工具无需作用域绑定：它不读取任何数据，工单归属在持久化时由服务端
// 依据会话确定。集中注册可以让测试直接覆盖生产接线路径，避免只验证替身注册表。
func newToolRegistry(
	reader crm.SubscriptionReader,
	customerID string,
) (*agenttool.Registry, error) {
	subscriptionTool, err := agenttool.NewSubscriptionTool(reader, customerID)
	if err != nil {
		return nil, err
	}
	return agenttool.NewRegistry(subscriptionTool, agenttool.NewDraftTicketTool())
}
