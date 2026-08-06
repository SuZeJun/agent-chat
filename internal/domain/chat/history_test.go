package chat

import (
	"testing"
	"time"
)

func TestMessageHistoryItemValidatesRunRelationship(t *testing.T) {
	now := time.Now().UTC()
	item := MessageHistoryItem{
		Message: Message{
			ID:             "assistant-1",
			ConversationID: "conversation-1",
			AgentRunID:     "run-1",
			Role:           MessageRoleAssistant,
			Content:        "请确认工单草稿",
			CreatedAt:      now,
		},
		RunID:     "run-1",
		RunStatus: RunStatusCompleted,
		RunResult: map[string]any{"nextAction": "confirm_ticket"},
	}
	if err := item.Validate(); err != nil {
		t.Fatalf("valid item rejected: %v", err)
	}

	item.RunID = "run-2"
	if err := item.Validate(); err == nil {
		t.Fatal("assistant item accepted a mismatched Run")
	}

	item.RunID = ""
	item.RunStatus = ""
	item.RunResult = nil
	if err := item.Validate(); err == nil {
		t.Fatal("assistant item accepted a missing Run")
	}
}

// 人工接管期间客户消息不触发 Run，历史仍必须可加载。
func TestMessageHistoryItemAcceptsCustomerMessageWithoutRun(t *testing.T) {
	now := time.Now().UTC()
	item := MessageHistoryItem{
		Message: Message{
			ID:              "customer-1",
			ConversationID:  "conversation-1",
			ClientMessageID: "client-1",
			Role:            MessageRoleCustomer,
			Content:         "那我等人工回复",
			CreatedAt:       now,
		},
	}
	if err := item.Validate(); err != nil {
		t.Fatalf("customer message during human handoff rejected: %v", err)
	}

	item.RunStatus = RunStatusCompleted
	if err := item.Validate(); err == nil {
		t.Fatal("history without a Run accepted Run fields")
	}
}

func TestRunSourceRejectsHistoryOutsideSourceBoundary(t *testing.T) {
	now := time.Now().UTC()
	source := RunSource{
		Run: AgentRun{
			ID:              "run-2",
			RequestID:       "request-2",
			ConversationID:  "conversation-1",
			SourceMessageID: "message-2",
			Status:          RunStatusRunning,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		Message: Message{
			ID:              "message-2",
			ConversationID:  "conversation-1",
			ClientMessageID: "client-2",
			Role:            MessageRoleCustomer,
			Content:         "帮我建个工单",
			CreatedAt:       now,
		},
		History: []Message{
			{
				ID:              "message-1",
				ConversationID:  "conversation-1",
				ClientMessageID: "client-1",
				Role:            MessageRoleCustomer,
				Content:         "账单导出一直没有反应",
				CreatedAt:       now.Add(-time.Minute),
			},
		},
		KnowledgeBaseID: "base-1",
		CustomerID:      "customer-1",
		Conversation:    ConversationStatusAIActive,
	}
	if err := source.Validate(); err != nil {
		t.Fatalf("valid history rejected: %v", err)
	}

	source.History[0].ConversationID = "conversation-2"
	if err := source.Validate(); err == nil {
		t.Fatal("cross-conversation history was accepted")
	}
	source.History[0].ConversationID = "conversation-1"
	source.History[0].CreatedAt = now.Add(time.Second)
	if err := source.Validate(); err == nil {
		t.Fatal("future history was accepted")
	}
}
