package chat

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentgraph "agent-chat/internal/agent/graph"
	domain "agent-chat/internal/domain/chat"
)

type fakeRunRepository struct {
	source          domain.RunSource
	beginCommand    domain.BeginRunAttempt
	beginErr        error
	progressEvents  []domain.EventDraft
	progressErr     error
	completeCommand domain.CompleteRunCommand
	completeErr     error
	failureCommand  domain.RecordRunFailureCommand
	failureErr      error
	mutex           sync.Mutex
	beginCalls      int
	completeCalls   int
	failureCalls    int
}

func (repository *fakeRunRepository) AppendRunProgress(
	_ context.Context,
	command domain.AppendRunProgressCommand,
) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.progressEvents = append(repository.progressEvents, command.Events...)
	return repository.progressErr
}

// progressOfType 返回指定类型的进度事件，供断言运行期发出的内容。
func (repository *fakeRunRepository) progressOfType(
	eventType domain.EventType,
) []domain.EventDraft {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	events := make([]domain.EventDraft, 0, len(repository.progressEvents))
	for _, event := range repository.progressEvents {
		if event.Type == eventType {
			events = append(events, event)
		}
	}
	return events
}

func (repository *fakeRunRepository) BeginRunAttempt(
	_ context.Context,
	command domain.BeginRunAttempt,
) (domain.RunSource, error) {
	repository.beginCalls++
	repository.beginCommand = command
	return repository.source, repository.beginErr
}

func (repository *fakeRunRepository) CompleteRun(
	_ context.Context,
	command domain.CompleteRunCommand,
) error {
	repository.completeCalls++
	repository.completeCommand = command
	return repository.completeErr
}

func (repository *fakeRunRepository) RecordRunFailure(
	_ context.Context,
	command domain.RecordRunFailureCommand,
) error {
	repository.failureCalls++
	repository.failureCommand = command
	return repository.failureErr
}

type fakeRuntimeFactory struct {
	runner          agentgraph.Runner
	err             error
	knowledgeBaseID string
	customerID      string
	calls           int
}

func (factory *fakeRuntimeFactory) Build(
	_ context.Context,
	knowledgeBaseID string,
	customerID string,
) (agentgraph.Runner, error) {
	factory.calls++
	factory.knowledgeBaseID = knowledgeBaseID
	factory.customerID = customerID
	return factory.runner, factory.err
}

type fakeGraphRunner struct {
	input  agentgraph.Input
	output agentgraph.Output
	err    error
	calls  int
}

// Run 复现真实 Graph 的 Observer 行为：先上报检索与决策，再逐块上报回答。
//
// 若替身不调用 Observer，执行器的进度接线就完全没有被测试覆盖——回答一次性
// 出现这类回归将无法被发现。
func (runner *fakeGraphRunner) Run(
	ctx context.Context,
	input agentgraph.Input,
) (agentgraph.Output, error) {
	runner.calls++
	runner.input = input
	if runner.err != nil {
		return agentgraph.Output{}, runner.err
	}

	observer := agentgraph.ObserverFromContext(ctx)
	observer.OnRetrieval(ctx, runner.output.Assessment.Evidence)
	observer.OnAssessment(ctx, runner.output.Assessment)
	for _, delta := range runner.deltas() {
		observer.OnAnswerDelta(ctx, delta)
	}
	return runner.output, nil
}

// deltas 把回答切成多块，模拟流式生成。
func (runner *fakeGraphRunner) deltas() []string {
	runes := []rune(runner.output.Answer)
	chunks := make([]string, 0, len(runes))
	for start := 0; start < len(runes); start += 4 {
		end := min(start+4, len(runes))
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func TestExecuteRunCompletesGraphResultAndEvents(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{source: testRunSource(domain.RunStatusRunning, now)}
	runner := &fakeGraphRunner{output: testGraphOutput()}
	factory := &fakeRuntimeFactory{runner: runner}
	executor := newTestExecutor(t, repository, factory, now)

	err := executor.ExecuteRun(context.Background(), ExecuteRunRequest{
		RunID:       "run-1",
		Attempt:     1,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("ExecuteRun returned error: %v", err)
	}
	if repository.beginCommand.Event.Type != domain.EventTypeRunStarted ||
		repository.beginCommand.Event.Payload["attempt"] != 1 {
		t.Fatalf("unexpected begin command: %#v", repository.beginCommand)
	}
	// 两个作用域都必须来自持久化的会话关系，而不是请求或模型输出：
	// 前者限定可检索的知识，后者限定工具可读取的客户数据。
	if factory.knowledgeBaseID != "base-1" ||
		factory.customerID != "customer-1" ||
		runner.input.Query != "如何重置密码？" {
		t.Fatalf(
			"unexpected Graph scope: kb=%s customer=%s input=%#v",
			factory.knowledgeBaseID,
			factory.customerID,
			runner.input,
		)
	}
	command := repository.completeCommand
	if command.RunID != "run-1" ||
		command.Message.AgentRunID != "run-1" ||
		command.Message.ConversationID != "conversation-1" ||
		command.Message.Role != domain.MessageRoleAssistant ||
		command.Message.Content != runner.output.Answer {
		t.Fatalf("unexpected completion command: %#v", command)
	}
	eventTypes := make([]domain.EventType, len(command.Events))
	for index, event := range command.Events {
		eventTypes[index] = event.Type
	}
	// 检索、决策与回答增量已在运行期发出，终态提交只补引用并收尾；
	// 若这里再出现 message.delta，客户端累积出的回答会翻倍。
	expectedTypes := []domain.EventType{
		domain.EventTypeMessageCitation,
		domain.EventTypeRunCompleted,
	}
	if !reflect.DeepEqual(eventTypes, expectedTypes) {
		t.Fatalf("unexpected completion events: %#v", eventTypes)
	}
	if len(repository.progressOfType(domain.EventTypeRetrievalCompleted)) != 1 ||
		len(repository.progressOfType(domain.EventTypeAnswerabilityDecided)) != 1 {
		t.Fatalf("progress events were not emitted: %#v", repository.progressEvents)
	}
	// 增量拼接必须等于持久化的回答，否则客户端看到的内容与最终结果不一致。
	var streamed strings.Builder
	for _, event := range repository.progressOfType(domain.EventTypeMessageDelta) {
		streamed.WriteString(event.Payload["delta"].(string))
	}
	if streamed.String() != runner.output.Answer {
		t.Fatalf("streamed answer %q differs from result %q", streamed.String(), runner.output.Answer)
	}
	if command.Result["answer"] != runner.output.Answer {
		t.Fatalf("Graph result was not persisted: %#v", command.Result)
	}
	if len(command.Steps) != 1 ||
		command.Steps[0].Name != "grounded_generate" ||
		command.Steps[0].PromptTokens != 120 ||
		command.Steps[0].CompletionTokens != 30 {
		t.Fatalf("Graph Trace was not mapped: %#v", command.Steps)
	}
	if repository.failureCalls != 0 {
		t.Fatal("successful execution recorded a failure")
	}
}

func TestExecuteRunSkipsTerminalRun(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeRunRepository{source: testRunSource(domain.RunStatusCompleted, now)}
	factory := &fakeRuntimeFactory{runner: &fakeGraphRunner{}}
	executor := newTestExecutor(t, repository, factory, now)

	if err := executor.ExecuteRun(context.Background(), ExecuteRunRequest{
		RunID:       "run-1",
		Attempt:     2,
		MaxAttempts: 5,
	}); err != nil {
		t.Fatalf("ExecuteRun returned error: %v", err)
	}
	if factory.calls != 0 ||
		repository.completeCalls != 0 ||
		repository.failureCalls != 0 {
		t.Fatal("terminal replay executed side effects")
	}
}

func TestExecuteRunSeparatesRetryAndTerminalFailure(t *testing.T) {
	tests := []struct {
		name        string
		attempt     int
		maxAttempts int
		retryable   bool
		terminal    bool
		eventType   domain.EventType
	}{
		{
			name:        "retryable before final attempt",
			attempt:     1,
			maxAttempts: 3,
			retryable:   true,
			terminal:    false,
			eventType:   domain.EventTypeRunStatus,
		},
		{
			name:        "retryable on final attempt",
			attempt:     3,
			maxAttempts: 3,
			retryable:   true,
			terminal:    true,
			eventType:   domain.EventTypeRunFailed,
		},
		{
			name:        "permanent before final attempt",
			attempt:     1,
			maxAttempts: 3,
			retryable:   false,
			terminal:    true,
			eventType:   domain.EventTypeRunFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			repository := &fakeRunRepository{source: testRunSource(domain.RunStatusRunning, now)}
			runner := &fakeGraphRunner{
				err: &agentgraph.Failure{
					Code:         "rag_provider_failed",
					RetryAllowed: test.retryable,
				},
			}
			executor := newTestExecutor(
				t,
				repository,
				&fakeRuntimeFactory{runner: runner},
				now,
			)

			err := executor.ExecuteRun(context.Background(), ExecuteRunRequest{
				RunID:       "run-1",
				Attempt:     test.attempt,
				MaxAttempts: test.maxAttempts,
			})
			var failure *Failure
			if !errors.As(err, &failure) ||
				failure.Code != "rag_provider_failed" ||
				failure.RetryAllowed != test.retryable {
				t.Fatalf("unexpected execution failure: %v", err)
			}
			if repository.failureCommand.Terminal != test.terminal ||
				repository.failureCommand.Event.Type != test.eventType ||
				repository.failureCommand.Attempt != test.attempt {
				t.Fatalf("unexpected failure command: %#v", repository.failureCommand)
			}
			if repository.completeCalls != 0 {
				t.Fatal("failed Graph completed the Run")
			}
		})
	}
}

func TestExecuteRunRejectsCompletionAfterConversationTakeover(t *testing.T) {
	now := time.Now().UTC()
	repository := &fakeRunRepository{
		source:      testRunSource(domain.RunStatusRunning, now),
		completeErr: domain.ErrInvalidState,
	}
	executor := newTestExecutor(
		t,
		repository,
		&fakeRuntimeFactory{runner: &fakeGraphRunner{output: testGraphOutput()}},
		now,
	)

	err := executor.ExecuteRun(context.Background(), ExecuteRunRequest{
		RunID:       "run-1",
		Attempt:     1,
		MaxAttempts: 5,
	})
	var failure *Failure
	if !errors.As(err, &failure) ||
		failure.Code != "conversation_not_ai_active" ||
		failure.RetryAllowed {
		t.Fatalf("unexpected failure: %v", err)
	}
	if !repository.failureCommand.Terminal ||
		repository.failureCommand.Event.Type != domain.EventTypeRunFailed {
		t.Fatalf("takeover did not terminate Run: %#v", repository.failureCommand)
	}
}

// TestExecuteRunConvergesRunWhenCompletionFails 保证提交失败也会写入失败状态。
//
// 提交阶段一旦直接返回错误而不记录失败，Job 耗尽重试后 Run 会永远停在 running，
// SSE 客户端也就永远等不到终态事件。
func TestExecuteRunConvergesRunWhenCompletionFails(t *testing.T) {
	tests := []struct {
		name        string
		completeErr error
		attempt     int
		code        string
		retryable   bool
		terminal    bool
		eventType   domain.EventType
	}{
		{
			name:        "invalid command is permanent and terminal",
			completeErr: domain.ErrInvalidCommand,
			attempt:     1,
			code:        "invalid_agent_run_completion",
			retryable:   false,
			terminal:    true,
			eventType:   domain.EventTypeRunFailed,
		},
		{
			name:        "transient database failure keeps retrying",
			completeErr: errors.New("database operation failed"),
			attempt:     1,
			code:        "complete_agent_run_failed",
			retryable:   true,
			terminal:    false,
			eventType:   domain.EventTypeRunStatus,
		},
		{
			name:        "transient failure terminates on the last attempt",
			completeErr: errors.New("database operation failed"),
			attempt:     5,
			code:        "complete_agent_run_failed",
			retryable:   true,
			terminal:    true,
			eventType:   domain.EventTypeRunFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			repository := &fakeRunRepository{
				source:      testRunSource(domain.RunStatusRunning, now),
				completeErr: test.completeErr,
			}
			executor := newTestExecutor(
				t,
				repository,
				&fakeRuntimeFactory{runner: &fakeGraphRunner{output: testGraphOutput()}},
				now,
			)

			err := executor.ExecuteRun(context.Background(), ExecuteRunRequest{
				RunID:       "run-1",
				Attempt:     test.attempt,
				MaxAttempts: 5,
			})
			var failure *Failure
			if !errors.As(err, &failure) ||
				failure.Code != test.code ||
				failure.RetryAllowed != test.retryable {
				t.Fatalf("unexpected failure: %v", err)
			}
			if repository.failureCommand.Terminal != test.terminal ||
				repository.failureCommand.Event.Type != test.eventType ||
				repository.failureCommand.ErrorCode != test.code {
				t.Fatalf("Run was not converged: %#v", repository.failureCommand)
			}
		})
	}
}

func testRunSource(status domain.RunStatus, now time.Time) domain.RunSource {
	return domain.RunSource{
		Run: domain.AgentRun{
			ID:              "run-1",
			RequestID:       "request-1",
			ConversationID:  "conversation-1",
			SourceMessageID: "message-1",
			Status:          status,
			CreatedAt:       now.Add(-time.Minute),
			UpdatedAt:       now,
		},
		Message: domain.Message{
			ID:              "message-1",
			ConversationID:  "conversation-1",
			ClientMessageID: "client-message-1",
			Role:            domain.MessageRoleCustomer,
			Content:         "如何重置密码？",
			CreatedAt:       now.Add(-time.Minute),
		},
		KnowledgeBaseID: "base-1",
		CustomerID:      "customer-1",
		Conversation:    domain.ConversationStatusAIActive,
	}
}

func testGraphOutput() agentgraph.Output {
	startedAt := time.Date(2026, 7, 25, 9, 59, 59, 0, time.UTC)
	return agentgraph.Output{
		Answer: "请在设置页重置密码。[S1]",
		Assessment: agentgraph.Assessment{
			Decision:   agentgraph.DecisionAnswerable,
			Reason:     "knowledge_support_sufficient",
			Confidence: 0.91,
			Evidence: []agentgraph.Evidence{{
				SourceID:   "S1",
				ChunkID:    "chunk-1",
				DocumentID: "document-1",
				VersionID:  "version-1",
				Score:      0.91,
				Rank:       1,
			}},
		},
		Citations: []agentgraph.Citation{{
			Evidence: agentgraph.Evidence{
				SourceID:   "S1",
				ChunkID:    "chunk-1",
				DocumentID: "document-1",
				VersionID:  "version-1",
				Score:      0.91,
				Rank:       1,
			},
			Excerpt: "请在设置页重置密码。",
		}},
		NodePath: []string{
			"validate_input",
			"retrieve_knowledge",
			"answerability_gate",
			"grounded_generate",
		},
		Trace: []agentgraph.TraceStep{
			{
				Name:             "grounded_generate",
				Component:        "ChatModel",
				ComponentType:    "deepseek/deepseek-v4-flash",
				Status:           "completed",
				StartedAt:        startedAt,
				CompletedAt:      startedAt.Add(250 * time.Millisecond),
				DurationMillis:   250,
				PromptTokens:     120,
				CompletionTokens: 30,
			},
		},
	}
}

func newTestExecutor(
	t *testing.T,
	repository RunRepository,
	factory RuntimeFactory,
	now time.Time,
) *Executor {
	t.Helper()
	executor, err := NewExecutor(
		repository,
		factory,
		&sequentialIDGenerator{},
		fixedClock{now: now},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("NewExecutor returned error: %v", err)
	}
	return executor
}
