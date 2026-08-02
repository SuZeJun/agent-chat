package graph

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	agenttool "agent-chat/internal/agent/tool"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fakePlanner struct {
	toolName string
	err      error
	calls    int
	messages []*schema.Message
}

func (planner *fakePlanner) Generate(
	_ context.Context,
	messages []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	planner.calls++
	planner.messages = messages
	if planner.err != nil {
		return nil, planner.err
	}
	if planner.toolName == "" {
		return schema.AssistantMessage("", nil), nil
	}
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{
				ID: "call-1",
				Function: schema.FunctionCall{
					Name:      planner.toolName,
					Arguments: "{}",
				},
			},
		},
	}, nil
}

type fakeInvoker struct {
	result string
	err    error
	calls  int
	names  []string
}

func (invoker *fakeInvoker) Invoke(
	_ context.Context,
	name string,
	_ string,
) (string, error) {
	invoker.calls++
	invoker.names = append(invoker.names, name)
	return invoker.result, invoker.err
}

func (invoker *fakeInvoker) Empty() bool { return false }

const testSubscriptionResult = `{"planName":"免费版","remainingApiCalls":683}`

// TestRuntimeRoutesToToolWhenPlannerSelectsIt 锁定工具分支的完整路径与来源约束。
func TestRuntimeRoutesToToolWhenPlannerSelectsIt(t *testing.T) {
	planner := &fakePlanner{toolName: agenttool.SubscriptionToolName}
	invoker := &fakeInvoker{result: testSubscriptionResult}
	chatModel := &fakeChatModel{answer: "你的免费版本月还剩 683 次调用。"}
	retriever := &fakeRetriever{}
	runtime := newTestRuntimeWithTools(t, retriever, chatModel, planner, invoker)

	output, err := runtime.Invoke(context.Background(), Input{Query: "我这个月还能调用多少次？"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}

	expectedPath := []string{
		nodeValidateInput,
		nodePlanAction,
		nodeInvokeTool,
		nodeExplainToolResult,
	}
	if !reflect.DeepEqual(output.NodePath, expectedPath) {
		t.Fatalf("unexpected node path: %#v", output.NodePath)
	}
	// 工具分支不得触碰知识检索：账户数据与知识库是两个独立的事实来源。
	if retriever.calls != 0 {
		t.Fatal("tool branch performed knowledge retrieval")
	}
	if output.Assessment.Decision != DecisionAnswerable ||
		output.Assessment.Reason != reasonToolResultSufficient {
		t.Fatalf("unexpected assessment: %#v", output.Assessment)
	}
	// 账户数据不是知识切片，不产生引用。
	if len(output.Citations) != 0 {
		t.Fatalf("tool answer must not carry knowledge citations: %#v", output.Citations)
	}
	if len(output.ToolCalls) != 1 ||
		output.ToolCalls[0].Name != agenttool.SubscriptionToolName ||
		output.ToolCalls[0].Status != toolStatusSucceeded {
		t.Fatalf("unexpected tool call record: %#v", output.ToolCalls)
	}
	// 工具结果必须原样作为不可信 JSON 进入 Prompt。
	prompt := chatModel.messages[len(chatModel.messages)-1].Content
	if !strings.Contains(prompt, `"accountData"`) ||
		!strings.Contains(prompt, "683") {
		t.Fatalf("tool result did not reach the prompt as data: %q", prompt)
	}
	// 数据归属必须随结果一起进入 Prompt。真机验证中，缺少该标注会让模型把当前
	// 客户的数据冠以问题中提到的其他客户身份陈述——数据未泄露，结论却是错的。
	if !strings.Contains(prompt, `"accountDataBelongsTo":"customer-1"`) {
		t.Fatalf("tool result reached the prompt without its owner: %q", prompt)
	}
}

// TestRuntimeFallsBackToKnowledgeWhenPlannerSelectsNoTool 保证规划节点不选工具时
// 退回知识链路，而不是失败。
func TestRuntimeFallsBackToKnowledgeWhenPlannerSelectsNoTool(t *testing.T) {
	planner := &fakePlanner{}
	invoker := &fakeInvoker{}
	chatModel := &fakeChatModel{answer: "请在设置页点击重置密码。[S1]"}
	retriever := &fakeRetriever{
		documents: []*schema.Document{testDocument("chunk-1", 0.91, 1)},
	}
	runtime := newTestRuntimeWithTools(t, retriever, chatModel, planner, invoker)

	output, err := runtime.Invoke(context.Background(), Input{Query: "如何重置密码？"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if invoker.calls != 0 {
		t.Fatal("no tool was selected but a tool was invoked")
	}
	if output.Assessment.Reason != reasonKnowledgeSupportSufficient {
		t.Fatalf("unexpected assessment: %#v", output.Assessment)
	}
	if len(output.Citations) != 1 {
		t.Fatalf("knowledge answer must carry citations: %#v", output.Citations)
	}
}

// TestRuntimeSkipsPlanningWithoutTools 保证未配置工具时不产生额外模型调用。
//
// 规划本身有成本；没有工具可选时这次调用不产生任何决策价值。
func TestRuntimeSkipsPlanningWithoutTools(t *testing.T) {
	planner := &fakePlanner{toolName: agenttool.SubscriptionToolName}
	chatModel := &fakeChatModel{answer: "请在设置页点击重置密码。[S1]"}
	retriever := &fakeRetriever{
		documents: []*schema.Document{testDocument("chunk-1", 0.91, 1)},
	}
	// 只传 planner 不传注册表，WithTools 应当整体降级。
	runtime := newTestRuntimeWithTools(t, retriever, chatModel, planner, nil)

	if _, err := runtime.Invoke(context.Background(), Input{Query: "如何重置密码？"}); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if planner.calls != 0 {
		t.Fatalf("planner was called without a tool registry: %d", planner.calls)
	}
}

// TestRuntimeConvertsPermanentToolFailureToDeterministicAnswer 锁定失败兜底。
//
// 工具失败不得交给模型解释：模型面对失败极易顺势编造一个看似合理的账户状态，
// 这正是「CRM 不可用时不得猜测订阅状态」要禁止的。
func TestRuntimeConvertsPermanentToolFailureToDeterministicAnswer(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		expected string
	}{
		{"not found", "subscription_not_found", toolNotFoundAnswer},
		{"not allowed", "tool_not_allowed", toolRejectedAnswer},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planner := &fakePlanner{toolName: agenttool.SubscriptionToolName}
			invoker := &fakeInvoker{
				err: agenttool.NewFailure(
					agenttool.SubscriptionToolName,
					test.code,
					false,
					errors.New("cause"),
				),
			}
			chatModel := &fakeChatModel{answer: "不应被调用"}
			runtime := newTestRuntimeWithTools(t, &fakeRetriever{}, chatModel, planner, invoker)

			output, err := runtime.Invoke(context.Background(), Input{Query: "我的套餐是什么？"})
			if err != nil {
				t.Fatalf("Invoke returned error: %v", err)
			}
			if output.Answer != test.expected {
				t.Fatalf("unexpected fallback answer: %q", output.Answer)
			}
			if chatModel.calls != 0 {
				t.Fatal("failed tool call reached the chat model")
			}
			if output.Assessment.Decision != DecisionUnanswerable ||
				output.Assessment.Reason != reasonToolExecutionFailed {
				t.Fatalf("unexpected assessment: %#v", output.Assessment)
			}
			if output.NextAction != NextActionRequestHumanSupport {
				t.Fatalf("unexpected next action: %q", output.NextAction)
			}
			if len(output.ToolCalls) != 1 ||
				output.ToolCalls[0].Status != toolStatusFailed ||
				output.ToolCalls[0].ErrorCode != test.code {
				t.Fatalf("tool failure was not recorded: %#v", output.ToolCalls)
			}
		})
	}
}

// TestRuntimePropagatesRetryableToolFailure 保证可重试失败上抛给 Job 队列，
// 而不是被兜底回复吞掉——吞掉会让一次网络抖动变成永久性的错误回答。
func TestRuntimePropagatesRetryableToolFailure(t *testing.T) {
	planner := &fakePlanner{toolName: agenttool.SubscriptionToolName}
	invoker := &fakeInvoker{
		err: agenttool.NewFailure(
			agenttool.SubscriptionToolName,
			"crm_unavailable",
			true,
			errors.New("cause"),
		),
	}
	runtime := newTestRuntimeWithTools(
		t,
		&fakeRetriever{},
		&fakeChatModel{},
		planner,
		invoker,
	)

	_, err := runtime.Invoke(context.Background(), Input{Query: "我的套餐是什么？"})
	assertFailure(t, err, "crm_unavailable", true)
}

func TestPlanningFailureIsClassifiedByCause(t *testing.T) {
	planner := &fakePlanner{err: retryableTestError{message: "provider busy"}}
	runtime := newTestRuntimeWithTools(
		t,
		&fakeRetriever{},
		&fakeChatModel{},
		planner,
		&fakeInvoker{},
	)

	_, err := runtime.Invoke(context.Background(), Input{Query: "我的套餐是什么？"})
	assertFailure(t, err, "tool_planning_failed", true)
}

func newTestRuntimeWithTools(
	t *testing.T,
	retriever *fakeRetriever,
	chatModel ChatModel,
	planner ToolPlanner,
	tools ToolInvoker,
) *Runtime {
	t.Helper()
	var options []RuntimeOption
	if tools == nil {
		options = append(options, WithTools(planner, nil, "customer-1"))
	} else {
		options = append(options, WithTools(planner, tools, "customer-1"))
	}
	runtime, err := NewRuntime(
		context.Background(),
		retriever,
		chatModel,
		DefaultConfig(),
		options...,
	)
	if err != nil {
		t.Fatalf("NewRuntime returned error: %v", err)
	}
	return runtime
}
