package chat

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	agentgraph "agent-chat/internal/agent/graph"
	domain "agent-chat/internal/domain/chat"
)

type fakeRunRepository struct {
	source          domain.RunSource
	beginCommand    domain.BeginRunAttempt
	beginErr        error
	completeCommand domain.CompleteRunCommand
	completeErr     error
	failureCommand  domain.RecordRunFailureCommand
	failureErr      error
	beginCalls      int
	completeCalls   int
	failureCalls    int
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
	calls           int
}

func (factory *fakeRuntimeFactory) Build(
	_ context.Context,
	knowledgeBaseID string,
) (agentgraph.Runner, error) {
	factory.calls++
	factory.knowledgeBaseID = knowledgeBaseID
	return factory.runner, factory.err
}

type fakeGraphRunner struct {
	input  agentgraph.Input
	output agentgraph.Output
	err    error
	calls  int
}

func (runner *fakeGraphRunner) Run(
	_ context.Context,
	input agentgraph.Input,
) (agentgraph.Output, error) {
	runner.calls++
	runner.input = input
	return runner.output, runner.err
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
	if factory.knowledgeBaseID != "base-1" ||
		runner.input.Query != "如何重置密码？" {
		t.Fatalf("unexpected Graph input: factory=%s input=%#v", factory.knowledgeBaseID, runner.input)
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
	expectedTypes := []domain.EventType{
		domain.EventTypeRetrievalCompleted,
		domain.EventTypeAnswerabilityDecided,
		domain.EventTypeMessageDelta,
		domain.EventTypeMessageCitation,
		domain.EventTypeRunCompleted,
	}
	if !reflect.DeepEqual(eventTypes, expectedTypes) {
		t.Fatalf("unexpected completion events: %#v", eventTypes)
	}
	if command.Result["answer"] != runner.output.Answer {
		t.Fatalf("Graph result was not persisted: %#v", command.Result)
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

func testRunSource(status domain.RunStatus, now time.Time) domain.RunSource {
	return domain.RunSource{
		Run: domain.AgentRun{
			ID:              "run-1",
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
		Conversation:    domain.ConversationStatusAIActive,
	}
}

func testGraphOutput() agentgraph.Output {
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
	)
	if err != nil {
		t.Fatalf("NewExecutor returned error: %v", err)
	}
	return executor
}
