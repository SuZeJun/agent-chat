package jobs

import (
	"context"
	"errors"
	"testing"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"
)

type fakeAgentRunExecutor struct {
	request application.ExecuteRunRequest
	err     error
	calls   int
}

func (executor *fakeAgentRunExecutor) ExecuteRun(
	_ context.Context,
	request application.ExecuteRunRequest,
) error {
	executor.calls++
	executor.request = request
	return executor.err
}

func TestAgentRunHandlerValidatesAndDispatchesPayload(t *testing.T) {
	executor := &fakeAgentRunExecutor{}
	handler, err := NewAgentRunHandler(executor)
	if err != nil {
		t.Fatalf("NewAgentRunHandler returned error: %v", err)
	}
	job := testJob("job-1", domain.AgentRunJobType, 2)
	job.MaxAttempts = 5
	job.IdempotencyKey = "run-1"
	job.Payload = []byte(`{"run_id":"run-1"}`)

	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if executor.request.RunID != "run-1" ||
		executor.request.Attempt != 2 ||
		executor.request.MaxAttempts != 5 {
		t.Fatalf("unexpected execution request: %#v", executor.request)
	}
}

func TestAgentRunHandlerRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name           string
		jobType        string
		idempotencyKey string
		payload        string
		expectedCode   string
	}{
		{
			name:         "wrong type",
			jobType:      "knowledge.index",
			payload:      `{"run_id":"run-1"}`,
			expectedCode: "invalid_job_type",
		},
		{
			name:         "unknown field",
			jobType:      domain.AgentRunJobType,
			payload:      `{"run_id":"run-1","secret":"x"}`,
			expectedCode: "invalid_job_payload",
		},
		{
			name:         "blank run",
			jobType:      domain.AgentRunJobType,
			payload:      `{"run_id":" "}`,
			expectedCode: "invalid_job_payload",
		},
		{
			name:           "idempotency mismatch",
			jobType:        domain.AgentRunJobType,
			idempotencyKey: "run-2",
			payload:        `{"run_id":"run-1"}`,
			expectedCode:   "job_idempotency_mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeAgentRunExecutor{}
			handler, err := NewAgentRunHandler(executor)
			if err != nil {
				t.Fatalf("NewAgentRunHandler returned error: %v", err)
			}
			job := testJob("job-1", test.jobType, 1)
			job.IdempotencyKey = test.idempotencyKey
			job.Payload = []byte(test.payload)

			err = handler.Handle(context.Background(), job)
			code, retryable := classifyHandlerError(err)
			if code != test.expectedCode || retryable {
				t.Fatalf("unexpected classification: code=%s retryable=%t", code, retryable)
			}
			if executor.calls != 0 {
				t.Fatal("invalid job reached executor")
			}
		})
	}
}

func TestAgentRunHandlerMapsApplicationFailure(t *testing.T) {
	tests := []struct {
		name      string
		retryable bool
	}{
		{name: "retryable", retryable: true},
		{name: "permanent", retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeAgentRunExecutor{
				err: &application.Failure{
					Code:         "rag_execution_failed",
					RetryAllowed: test.retryable,
				},
			}
			handler, err := NewAgentRunHandler(executor)
			if err != nil {
				t.Fatalf("NewAgentRunHandler returned error: %v", err)
			}
			job := testJob("job-1", domain.AgentRunJobType, 1)
			job.Payload = []byte(`{"run_id":"run-1"}`)

			err = handler.Handle(context.Background(), job)
			code, retryable := classifyHandlerError(err)
			if code != "rag_execution_failed" || retryable != test.retryable {
				t.Fatalf("unexpected classification: code=%s retryable=%t", code, retryable)
			}
			if errors.Is(err, context.Canceled) {
				t.Fatal("unexpected cancellation")
			}
		})
	}
}
