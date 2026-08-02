package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	einoretriever "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	nodeValidateInput     = "validate_input"
	nodePlanAction        = "plan_action"
	nodeInvokeTool        = "invoke_tool"
	nodeExplainToolResult = "explain_tool_result"
	nodeRetrieveKnowledge = "retrieve_knowledge"
	nodeAnswerabilityGate = "answerability_gate"
	nodeGroundedGenerate  = "grounded_generate"
	nodeAskClarification  = "ask_clarification"
	nodeRefuseAnswer      = "refuse_answer"
)

// Runtime 持有编译后的 Eino RAG Graph，可安全并发执行。
type Runtime struct {
	runnable compose.Runnable[Input, Output]
}

// RuntimeOption 配置 Runtime 的可选能力。
type RuntimeOption func(*dependencies)

// WithTools 启用工具规划分支。
//
// planner 与 tools 必须同时提供：只有 planner 而无注册表则无法执行工具，只有
// 注册表而无 planner 则无人选择工具。任一缺失时静默降级为纯知识链路。
//
// dataOwner 是工具数据的归属客户，会随工具结果一起进入 Prompt：工具永远只返回
// 该客户的数据，但问题里可能提到别的客户名，不标明归属会让模型把当前客户的数据
// 冠以他人身份陈述。
func WithTools(planner ToolPlanner, tools ToolInvoker, dataOwner string) RuntimeOption {
	return func(deps *dependencies) {
		if planner == nil || tools == nil {
			return
		}
		deps.planner = planner
		deps.tools = tools
		deps.dataOwner = dataOwner
	}
}

// NewRuntime 创建检索、Answerability 和三路响应节点组成的 Eino Graph。
//
// 未提供工具时保留纯知识链路的行为：规划节点直接放行，不产生额外模型调用。
func NewRuntime(
	ctx context.Context,
	retriever einoretriever.Retriever,
	chatModel ChatModel,
	config Config,
	options ...RuntimeOption,
) (*Runtime, error) {
	if retriever == nil {
		return nil, errors.New("RAG retriever is required")
	}
	if chatModel == nil {
		return nil, errors.New("RAG chat model is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	deps := dependencies{
		retriever: retriever,
		chatModel: chatModel,
		config:    config,
	}
	for _, option := range options {
		option(&deps)
	}

	graph := compose.NewGraph[Input, Output]()
	if err := graph.AddLambdaNode(
		nodeValidateInput,
		compose.InvokableLambda(deps.validateInput),
		compose.WithNodeName(nodeValidateInput),
	); err != nil {
		return nil, fmt.Errorf("add validate input node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodePlanAction,
		compose.InvokableLambda(deps.planAction),
		compose.WithNodeName(nodePlanAction),
	); err != nil {
		return nil, fmt.Errorf("add plan action node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodeInvokeTool,
		compose.InvokableLambda(deps.invokeTool),
		compose.WithNodeName(nodeInvokeTool),
	); err != nil {
		return nil, fmt.Errorf("add invoke tool node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodeExplainToolResult,
		compose.InvokableLambda(deps.explainToolResult),
		compose.WithNodeName(nodeExplainToolResult),
	); err != nil {
		return nil, fmt.Errorf("add explain tool result node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodeRetrieveKnowledge,
		compose.InvokableLambda(deps.retrieveKnowledge),
		compose.WithNodeName(nodeRetrieveKnowledge),
	); err != nil {
		return nil, fmt.Errorf("add retrieve knowledge node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodeAnswerabilityGate,
		compose.InvokableLambda(deps.answerabilityGate),
		compose.WithNodeName(nodeAnswerabilityGate),
	); err != nil {
		return nil, fmt.Errorf("add answerability gate node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodeGroundedGenerate,
		compose.InvokableLambda(deps.groundedGenerate),
		compose.WithNodeName(nodeGroundedGenerate),
	); err != nil {
		return nil, fmt.Errorf("add grounded generate node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodeAskClarification,
		compose.InvokableLambda(askClarification),
		compose.WithNodeName(nodeAskClarification),
	); err != nil {
		return nil, fmt.Errorf("add ask clarification node: %w", err)
	}
	if err := graph.AddLambdaNode(
		nodeRefuseAnswer,
		compose.InvokableLambda(refuseAnswer),
		compose.WithNodeName(nodeRefuseAnswer),
	); err != nil {
		return nil, fmt.Errorf("add refuse answer node: %w", err)
	}

	for _, edge := range [][2]string{
		{compose.START, nodeValidateInput},
		{nodeValidateInput, nodePlanAction},
		{nodeInvokeTool, nodeExplainToolResult},
		{nodeRetrieveKnowledge, nodeAnswerabilityGate},
		{nodeExplainToolResult, compose.END},
		{nodeGroundedGenerate, compose.END},
		{nodeAskClarification, compose.END},
		{nodeRefuseAnswer, compose.END},
	} {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, fmt.Errorf("add graph edge %s -> %s: %w", edge[0], edge[1], err)
		}
	}
	actionBranch := compose.NewGraphBranch(
		routeAction,
		map[string]bool{
			nodeInvokeTool:        true,
			nodeRetrieveKnowledge: true,
		},
	)
	if err := graph.AddBranch(nodePlanAction, actionBranch); err != nil {
		return nil, fmt.Errorf("add action branch: %w", err)
	}
	branch := compose.NewGraphBranch(
		routeAnswerability,
		map[string]bool{
			nodeGroundedGenerate: true,
			nodeAskClarification: true,
			nodeRefuseAnswer:     true,
		},
	)
	if err := graph.AddBranch(nodeAnswerabilityGate, branch); err != nil {
		return nil, fmt.Errorf("add answerability branch: %w", err)
	}

	runnable, err := graph.Compile(ctx, compose.WithGraphName("knowledge_rag"))
	if err != nil {
		return nil, fmt.Errorf("compile RAG graph: %w", err)
	}
	return &Runtime{runnable: runnable}, nil
}

// Invoke 执行编译后的 Graph；Callbacks 等 Eino 运行选项可由上层注入。
func (runtime *Runtime) Invoke(
	ctx context.Context,
	input Input,
	options ...compose.Option,
) (Output, error) {
	return runtime.runnable.Invoke(ctx, input, options...)
}

// Run 使用默认 Eino 运行选项执行 Graph，供 Application Runtime Port 调用。
func (runtime *Runtime) Run(ctx context.Context, input Input) (Output, error) {
	collector := newTraceCollector()
	output, err := runtime.Invoke(
		ctx,
		input,
		compose.WithCallbacks(collector.handler()),
	)
	output.Trace = collector.snapshot()
	return output, err
}

// validateInput 在任何检索或模型调用前规范化并限制用户问题。
func (deps dependencies) validateInput(
	_ context.Context,
	input Input,
) (runState, error) {
	query, err := normalizeQuery(input.Query)
	if err != nil {
		return runState{}, newFailure("invalid_rag_input", false, err)
	}
	return runState{
		query:    query,
		nodePath: []string{nodeValidateInput},
	}, nil
}

// retrieveKnowledge 调用绑定当前知识库的 Retriever，并校验证据排序和上下文上限。
func (deps dependencies) retrieveKnowledge(
	ctx context.Context,
	state runState,
) (runState, error) {
	documents, err := deps.retriever.Retrieve(ctx, state.query)
	if err != nil {
		return runState{}, newFailure("rag_retrieval_failed", retryAllowed(err), err)
	}
	sources, err := selectSources(
		documents,
		deps.config.MaxContextDocuments,
		deps.config.MaxContextRunes,
	)
	if err != nil {
		return runState{}, newFailure("invalid_rag_evidence", false, err)
	}
	state.sources = sources
	state.nodePath = append(state.nodePath, nodeRetrieveKnowledge)
	ObserverFromContext(ctx).OnRetrieval(ctx, evidenceOf(sources))
	return state, nil
}

// answerabilityGate 生成确定性 Assessment，后续分支只能读取该结果。
func (deps dependencies) answerabilityGate(
	ctx context.Context,
	state runState,
) (runState, error) {
	state.assessment = assessAnswerability(state.sources, deps.config)
	state.nodePath = append(state.nodePath, nodeAnswerabilityGate)
	ObserverFromContext(ctx).OnAssessment(ctx, state.assessment)
	return state, nil
}

// routeAnswerability 将三类决策映射到生成、澄清或拒答节点，不允许隐式兜底生成。
func routeAnswerability(
	_ context.Context,
	state runState,
) (string, error) {
	switch state.assessment.Decision {
	case DecisionAnswerable:
		return nodeGroundedGenerate, nil
	case DecisionNeedsClarification:
		return nodeAskClarification, nil
	case DecisionUnanswerable:
		return nodeRefuseAnswer, nil
	default:
		return "", newFailure(
			"invalid_answerability_decision",
			false,
			errors.New("unsupported answerability decision"),
		)
	}
}

// groundedGenerate 仅在 Gate 允许时调用模型，并拒绝没有有效来源标记的回答。
func (deps dependencies) groundedGenerate(
	ctx context.Context,
	state runState,
) (Output, error) {
	messages, err := buildPrompt(state.query, state.sources)
	if err != nil {
		return Output{}, newFailure("rag_prompt_failed", false, err)
	}
	answer, err := deps.streamAnswer(ctx, messages)
	if err != nil {
		return Output{}, err
	}
	// 引用只能在拿到完整回答后解析：来源标记可能落在任意两个增量的交界处。
	citations, err := citationsFromAnswer(answer, state.sources)
	if err != nil {
		return Output{}, newFailure("invalid_rag_answer", true, err)
	}
	state.answer = strings.TrimSpace(answer)
	state.citations = citations
	state.nodePath = append(state.nodePath, nodeGroundedGenerate)
	return state.output(), nil
}

// streamAnswer 消费模型流并逐块上报增量，返回拼接后的完整回答。
//
// 单个增量不做角色校验：供应商通常只在首块携带 Role，后续块仅有内容；
// 校验放在流结束后针对完整结果进行。
func (deps dependencies) streamAnswer(
	ctx context.Context,
	messages []*schema.Message,
) (string, error) {
	stream, err := deps.chatModel.Stream(ctx, messages)
	if err != nil {
		return "", newFailure("rag_generation_failed", retryAllowed(err), err)
	}
	defer stream.Close()

	observer := ObserverFromContext(ctx)
	var answer strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", newFailure("rag_generation_failed", retryAllowed(err), err)
		}
		if chunk == nil || chunk.Content == "" {
			continue
		}
		answer.WriteString(chunk.Content)
		observer.OnAnswerDelta(ctx, chunk.Content)
	}
	if strings.TrimSpace(answer.String()) == "" {
		return "", newFailure(
			"invalid_rag_answer",
			true,
			errors.New("chat model returned empty answer"),
		)
	}
	return answer.String(), nil
}

// askClarification 返回稳定追问文案，不调用模型，也不生成引用。
func askClarification(
	ctx context.Context,
	state runState,
) (Output, error) {
	state.answer = "我找到了可能相关的知识，但现有信息不足以可靠判断。请补充具体产品、操作步骤、错误信息或期望结果。"
	state.nextAction = NextActionProvideDetails
	state.nodePath = append(state.nodePath, nodeAskClarification)
	// 非流式分支同样经由 Observer 发出回答，使三条分支的事件形状保持一致。
	ObserverFromContext(ctx).OnAnswerDelta(ctx, state.answer)
	return state.output(), nil
}

// refuseAnswer 在证据不足时明确拒绝猜测，并提示转人工路径。
func refuseAnswer(
	ctx context.Context,
	state runState,
) (Output, error) {
	state.answer = "当前知识库没有足够信息支持可靠回答，我不会猜测。你可以补充更多信息，或联系人工支持。"
	state.nextAction = NextActionRequestHumanSupport
	state.nodePath = append(state.nodePath, nodeRefuseAnswer)
	ObserverFromContext(ctx).OnAnswerDelta(ctx, state.answer)
	return state.output(), nil
}
