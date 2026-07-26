package jobs

import (
	"context"
	"errors"
	"testing"

	"agent-chat/internal/application/knowledgeindex"
	domain "agent-chat/internal/domain/knowledge"
)

type fakeKnowledgeIndexer struct {
	versionID string
	err       error
}

func (indexer *fakeKnowledgeIndexer) IndexVersion(_ context.Context, versionID string) error {
	indexer.versionID = versionID
	return indexer.err
}

func TestKnowledgeIndexHandlerValidatesAndDispatchesPayload(t *testing.T) {
	indexer := &fakeKnowledgeIndexer{}
	handler, err := NewKnowledgeIndexHandler(indexer)
	if err != nil {
		t.Fatalf("NewKnowledgeIndexHandler returned error: %v", err)
	}
	job := testJob("job-1", domain.IndexJobType, 1)
	job.IdempotencyKey = "version-1"
	job.Payload = []byte(`{"version_id":"version-1"}`)

	if err := handler.Handle(context.Background(), job); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if indexer.versionID != "version-1" {
		t.Fatalf("unexpected version ID: %s", indexer.versionID)
	}
}

func TestKnowledgeIndexHandlerRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name           string
		jobType        string
		idempotencyKey string
		payload        string
		expectedCode   string
	}{
		{
			name:         "wrong type",
			jobType:      "agent.run",
			payload:      `{"version_id":"version-1"}`,
			expectedCode: "invalid_job_type",
		},
		{
			name:         "unknown field",
			jobType:      domain.IndexJobType,
			payload:      `{"version_id":"version-1","secret":"x"}`,
			expectedCode: "invalid_job_payload",
		},
		{
			name:         "blank version",
			jobType:      domain.IndexJobType,
			payload:      `{"version_id":" "}`,
			expectedCode: "invalid_job_payload",
		},
		{
			name:           "idempotency mismatch",
			jobType:        domain.IndexJobType,
			idempotencyKey: "version-2",
			payload:        `{"version_id":"version-1"}`,
			expectedCode:   "job_idempotency_mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			indexer := &fakeKnowledgeIndexer{}
			handler, err := NewKnowledgeIndexHandler(indexer)
			if err != nil {
				t.Fatalf("NewKnowledgeIndexHandler returned error: %v", err)
			}
			job := testJob("job-1", test.jobType, 1)
			job.IdempotencyKey = test.idempotencyKey
			job.Payload = []byte(test.payload)

			err = handler.Handle(context.Background(), job)
			code, retryable := classifyHandlerError(err)
			if code != test.expectedCode || retryable {
				t.Fatalf("unexpected classification: code=%s retryable=%t", code, retryable)
			}
			if indexer.versionID != "" {
				t.Fatalf("invalid payload reached indexer: %s", indexer.versionID)
			}
		})
	}
}

func TestKnowledgeIndexHandlerMapsApplicationFailure(t *testing.T) {
	tests := []struct {
		name      string
		retryable bool
	}{
		{name: "retryable", retryable: true},
		{name: "permanent", retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			indexer := &fakeKnowledgeIndexer{
				err: &knowledgeindex.Failure{
					Code:         "embedding_failed",
					RetryAllowed: test.retryable,
				},
			}
			handler, err := NewKnowledgeIndexHandler(indexer)
			if err != nil {
				t.Fatalf("NewKnowledgeIndexHandler returned error: %v", err)
			}
			job := testJob("job-1", domain.IndexJobType, 1)
			job.Payload = []byte(`{"version_id":"version-1"}`)

			err = handler.Handle(context.Background(), job)
			if err == nil {
				t.Fatal("expected an error")
			}
			code, retryable := classifyHandlerError(err)
			if code != "embedding_failed" || retryable != test.retryable {
				t.Fatalf("unexpected classification: code=%s retryable=%t", code, retryable)
			}
			if errors.Is(err, context.Canceled) {
				t.Fatal("unexpected cancellation")
			}
		})
	}
}
