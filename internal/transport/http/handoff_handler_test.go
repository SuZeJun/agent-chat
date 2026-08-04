package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"

	"github.com/gin-gonic/gin"
)

type fakeHandoffService struct {
	requestCustomerID string
	requestID         string
	requestReason     string
	queueAgentID      string
	eventActorID      string
	eventAfter        int
	eventPage         domain.ConversationEventPage
}

func (service *fakeHandoffService) RequestHandoff(_ context.Context, customerID, conversationID, reason string) (domain.HandoffConversation, error) {
	service.requestCustomerID, service.requestID, service.requestReason = customerID, conversationID, reason
	now := time.Now().UTC()
	return domain.HandoffConversation{
		Conversation: domain.Conversation{ID: conversationID, Status: domain.ConversationStatusWaitingHuman},
		Summary:      domain.HandoffSummary{CustomerRequest: reason, ConfirmedFacts: []string{}, UnresolvedQuestions: []string{reason}, RiskSignals: []string{}, Citations: []domain.HandoffCitation{}, ToolCalls: []domain.HandoffToolCall{}, RecommendedAction: "人工处理", CreatedAt: now, UpdatedAt: now},
	}, nil
}

func (*fakeHandoffService) SendCustomerMessage(context.Context, application.Request) (domain.Message, bool, error) {
	return domain.Message{}, false, nil
}

func (service *fakeHandoffService) ListQueue(_ context.Context, agentID string) ([]domain.HandoffConversation, error) {
	service.queueAgentID = agentID
	return []domain.HandoffConversation{}, nil
}

func (*fakeHandoffService) GetConversation(context.Context, string, string) (domain.HandoffConversation, error) {
	return domain.HandoffConversation{}, nil
}

func (*fakeHandoffService) Takeover(context.Context, string, string) (domain.HandoffConversation, error) {
	return domain.HandoffConversation{}, nil
}

func (*fakeHandoffService) SendAgentMessage(context.Context, string, string, string) (domain.Message, error) {
	return domain.Message{}, nil
}

func (*fakeHandoffService) ResumeAI(context.Context, string, string) (domain.HandoffConversation, error) {
	return domain.HandoffConversation{}, nil
}

func (service *fakeHandoffService) ReadCustomerEvents(_ context.Context, customerID, _ string, after int) (domain.ConversationEventPage, error) {
	service.eventActorID, service.eventAfter = customerID, after
	return service.eventPage, nil
}

func (service *fakeHandoffService) ReadAgentEvents(_ context.Context, agentID, _ string, after int) (domain.ConversationEventPage, error) {
	service.eventActorID, service.eventAfter = agentID, after
	return service.eventPage, nil
}

func TestRequestHandoffAPIUsesCustomerScope(t *testing.T) {
	service := &fakeHandoffService{}
	router := newHandoffTestRouter(service)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/conversation-1/handoff", bytes.NewBufferString(`{"reason":"请转人工"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(customerIDHeader, "customer-1")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if service.requestCustomerID != "customer-1" || service.requestID != "conversation-1" || service.requestReason != "请转人工" {
		t.Fatalf("unexpected request scope: %#v", service)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, exists := payload["summary"]; exists {
		t.Fatalf("agent-only handoff summary leaked to customer: %#v", payload)
	}
}

func TestAgentQueueRequiresServerAgentIdentity(t *testing.T) {
	service := &fakeHandoffService{}
	router := newHandoffTestRouter(service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agent/conversations", nil))
	if response.Code != http.StatusUnauthorized || service.queueAgentID != "" {
		t.Fatalf("unauthenticated queue access: status=%d agent=%q", response.Code, service.queueAgentID)
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/conversations", nil)
	request.Header.Set(agentIDHeader, "agent-1")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.queueAgentID != "agent-1" {
		t.Fatalf("agent queue access failed: status=%d agent=%q", response.Code, service.queueAgentID)
	}
}

func TestCustomerEventsHideAgentIdentifiers(t *testing.T) {
	now := time.Now().UTC()
	service := &fakeHandoffService{eventPage: domain.ConversationEventPage{
		ConversationID: "conversation-1", Status: domain.ConversationStatusHumanActive, AssignedAgentID: "agent-secret",
		Events: []domain.ConversationEvent{{
			ID: "event-2", ConversationID: "conversation-1", Sequence: 2, Type: domain.ConversationEventTakenOver,
			ActorType: domain.ConversationActorAgent, ActorID: "agent-secret", Payload: map[string]any{"status": "human_active"}, CreatedAt: now,
		}},
	}}
	router := newHandoffTestRouter(service)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/conversation-1/events?after=1", nil)
	request.Header.Set(customerIDHeader, "customer-1")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items := payload["items"].([]any)
	item := items[0].(map[string]any)
	if _, exists := payload["assignedAgentId"]; exists {
		t.Fatalf("assigned agent leaked to customer: %#v", payload)
	}
	if _, exists := item["actorId"]; exists {
		t.Fatalf("event actor ID leaked to customer: %#v", item)
	}
	if service.eventActorID != "customer-1" || service.eventAfter != 1 {
		t.Fatalf("event scope was not forwarded: actor=%q after=%d", service.eventActorID, service.eventAfter)
	}
}

func newHandoffTestRouter(service HandoffService) http.Handler {
	router := newKnowledgeTestRouter(nil, nil)
	engine, ok := router.(*gin.Engine)
	if !ok {
		panic("test router is not a Gin engine")
	}
	registerHandoffRoutes(engine, service)
	return engine
}
