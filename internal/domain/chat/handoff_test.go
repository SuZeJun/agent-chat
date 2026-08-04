package chat

import (
	"testing"
	"time"
)

func TestBuildHandoffSummaryUsesPersistedConversationFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	summary, err := BuildHandoffSummary("conversation-1", HandoffContext{
		Messages: []Message{
			{
				ID: "message-1", ConversationID: "conversation-1", ClientMessageID: "client-1",
				Role: MessageRoleCustomer, Content: "账单导出失败，比较紧急，想退款", CreatedAt: now.Add(-time.Minute),
			},
		},
		Citations: []HandoffCitation{
			{SourceID: "source-1", Title: "账单导出", Excerpt: "导出任务通常需要两分钟。", DocumentID: "document-1", VersionID: "version-1"},
			{SourceID: "source-1", Title: "重复引用", Excerpt: "不应重复。", DocumentID: "document-1", VersionID: "version-1"},
		},
		ToolCalls: []HandoffToolCall{{Name: "lookup_invoice", Status: "completed"}},
	}, now)
	if err != nil {
		t.Fatalf("BuildHandoffSummary: %v", err)
	}
	if summary.CustomerRequest != "账单导出失败，比较紧急，想退款" {
		t.Fatalf("unexpected customer request: %q", summary.CustomerRequest)
	}
	if len(summary.ConfirmedFacts) != 1 || len(summary.Citations) != 1 {
		t.Fatalf("citations were not deduplicated: %#v", summary)
	}
	if len(summary.ToolCalls) != 1 || summary.ToolCalls[0].Name != "lookup_invoice" {
		t.Fatalf("tool calls were not preserved: %#v", summary.ToolCalls)
	}
	if !containsString(summary.RiskSignals, "紧急请求") || !containsString(summary.RiskSignals, "退款或账务风险") {
		t.Fatalf("risk signals were not detected: %#v", summary.RiskSignals)
	}
}

func TestBuildHandoffSummaryRejectsCrossConversationMessage(t *testing.T) {
	now := time.Now().UTC()
	_, err := BuildHandoffSummary("conversation-1", HandoffContext{Messages: []Message{{
		ID: "message-1", ConversationID: "conversation-2", ClientMessageID: "client-1",
		Role: MessageRoleCustomer, Content: "不能泄露到其他会话", CreatedAt: now,
	}}}, now)
	if err == nil {
		t.Fatal("cross-conversation message was accepted")
	}
}

func TestBuildHandoffSummaryKeepsEmptyCollectionsAsJSONArrays(t *testing.T) {
	summary, err := BuildHandoffSummary("conversation-1", HandoffContext{}, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildHandoffSummary: %v", err)
	}
	if summary.ConfirmedFacts == nil || summary.RiskSignals == nil || summary.Citations == nil || summary.ToolCalls == nil {
		t.Fatalf("empty summary collections must not be nil: %#v", summary)
	}
}

func TestConversationEventValidation(t *testing.T) {
	event := ConversationEvent{
		ID: "event-1", ConversationID: "conversation-1", Sequence: 1,
		Type: ConversationEventTakenOver, ActorType: ConversationActorAgent,
		ActorID: "agent-1", Payload: map[string]any{"status": "human_active"}, CreatedAt: time.Now().UTC(),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	event.Sequence = 0
	if err := event.Validate(); err == nil {
		t.Fatal("non-positive event sequence was accepted")
	}
	event.Sequence = 1
	event.ActorID = ""
	if err := event.Validate(); err == nil {
		t.Fatal("agent event without actor ID was accepted")
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
