package chat

import (
	"testing"
	"time"
)

func TestStartRunSubmissionValidate(t *testing.T) {
	now := time.Now().UTC()
	submission := testSubmission(now)
	if err := submission.Validate(); err != nil {
		t.Fatalf("valid submission rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*StartRunSubmission)
	}{
		{
			name: "source is not customer",
			mutate: func(value *StartRunSubmission) {
				value.Message.Role = MessageRoleAssistant
				value.Message.ClientMessageID = ""
			},
		},
		{
			name: "run belongs to another message",
			mutate: func(value *StartRunSubmission) {
				value.Run.SourceMessageID = "message-2"
			},
		},
		{
			name: "run is already running",
			mutate: func(value *StartRunSubmission) {
				value.Run.Status = RunStatusRunning
			},
		},
		{
			name: "initial sequence is not one",
			mutate: func(value *StartRunSubmission) {
				value.Event.Sequence = 2
			},
		},
		{
			name: "initial event is not pending",
			mutate: func(value *StartRunSubmission) {
				value.Event.Payload["status"] = string(RunStatusRunning)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := testSubmission(now)
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestConversationStatusValid(t *testing.T) {
	for _, status := range []ConversationStatus{
		ConversationStatusAIActive,
		ConversationStatusWaitingHuman,
		ConversationStatusHumanActive,
		ConversationStatusClosed,
	} {
		if !status.Valid() {
			t.Fatalf("known status rejected: %s", status)
		}
	}
	if ConversationStatus("unknown").Valid() {
		t.Fatal("unknown status accepted")
	}
}

func TestRunExecutionCommandsValidate(t *testing.T) {
	now := time.Now().UTC()
	begin := BeginRunAttempt{
		RunID:   "run-1",
		Attempt: 1,
		Event: EventDraft{
			ID:        "event-start",
			Type:      EventTypeRunStarted,
			Payload:   map[string]any{"attempt": 1},
			CreatedAt: now,
		},
	}
	if err := begin.Validate(); err != nil {
		t.Fatalf("valid begin command rejected: %v", err)
	}

	completion := CompleteRunCommand{
		RunID: "run-1",
		Message: Message{
			ID:             "message-assistant",
			ConversationID: "conversation-1",
			AgentRunID:     "run-1",
			Role:           MessageRoleAssistant,
			Content:        "回答",
			CreatedAt:      now,
		},
		Result: map[string]any{"answer": "回答"},
		Events: []EventDraft{
			testEventDraft("event-retrieval", EventTypeRetrievalCompleted, now),
			testEventDraft("event-gate", EventTypeAnswerabilityDecided, now),
			testEventDraft("event-delta", EventTypeMessageDelta, now),
			testEventDraft("event-completed", EventTypeRunCompleted, now),
		},
		CompletedAt: now,
	}
	if err := completion.Validate(); err != nil {
		t.Fatalf("valid completion rejected: %v", err)
	}

	failure := RecordRunFailureCommand{
		RunID:      "run-1",
		Attempt:    1,
		ErrorCode:  "rag_execution_failed",
		Terminal:   true,
		Event:      testEventDraft("event-failed", EventTypeRunFailed, now),
		OccurredAt: now,
	}
	if err := failure.Validate(); err != nil {
		t.Fatalf("valid failure command rejected: %v", err)
	}

	failure.Event.Type = EventTypeRunStatus
	if err := failure.Validate(); err == nil {
		t.Fatal("terminal failure accepted retry event")
	}
}

func testSubmission(now time.Time) StartRunSubmission {
	return StartRunSubmission{
		CustomerID: "customer-1",
		Message: Message{
			ID:              "message-1",
			ConversationID:  "conversation-1",
			ClientMessageID: "client-message-1",
			Role:            MessageRoleCustomer,
			Content:         "如何重置密码？",
			CreatedAt:       now,
		},
		Run: AgentRun{
			ID:              "run-1",
			RequestID:       "request-1",
			ConversationID:  "conversation-1",
			SourceMessageID: "message-1",
			Status:          RunStatusPending,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		Event: RunEvent{
			ID:        "event-1",
			RunID:     "run-1",
			Sequence:  1,
			Type:      EventTypeRunStatus,
			Payload:   map[string]any{"status": string(RunStatusPending)},
			CreatedAt: now,
		},
		JobID: "job-1",
	}
}

func testEventDraft(id string, eventType EventType, now time.Time) EventDraft {
	return EventDraft{
		ID:        id,
		Type:      eventType,
		Payload:   map[string]any{},
		CreatedAt: now,
	}
}
