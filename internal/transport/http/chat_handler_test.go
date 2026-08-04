package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"

	"github.com/gin-gonic/gin"
)

type fakeConversationCreator struct {
	request application.CreateConversationRequest
	result  application.CreateConversationResult
	err     error
}

func (creator *fakeConversationCreator) Create(
	_ context.Context,
	request application.CreateConversationRequest,
) (application.CreateConversationResult, error) {
	creator.request = request
	return creator.result, creator.err
}

type fakeMessageSender struct {
	request application.Request
	result  application.Result
	err     error
}

type fakeMessageHistoryReader struct {
	request application.MessageHistoryRequest
	page    domain.MessageHistoryPage
	err     error
}

func (reader *fakeMessageHistoryReader) ReadMessageHistory(
	_ context.Context,
	request application.MessageHistoryRequest,
) (domain.MessageHistoryPage, error) {
	reader.request = request
	return reader.page, reader.err
}

func (sender *fakeMessageSender) SendMessage(
	_ context.Context,
	request application.Request,
) (application.Result, error) {
	sender.request = request
	return sender.result, sender.err
}

type fakeRunEventReader struct {
	requests []application.EventRequest
	pages    []domain.RunEventPage
	err      error
}

type fakeRunTraceReader struct {
	runID string
	trace domain.RunTraceSnapshot
	err   error
}

func (reader *fakeRunTraceReader) GetRunTrace(
	_ context.Context,
	runID string,
) (domain.RunTraceSnapshot, error) {
	reader.runID = runID
	return reader.trace, reader.err
}

func (reader *fakeRunEventReader) ReadEvents(
	_ context.Context,
	request application.EventRequest,
) (domain.RunEventPage, error) {
	reader.requests = append(reader.requests, request)
	if reader.err != nil {
		return domain.RunEventPage{}, reader.err
	}
	page := reader.pages[0]
	reader.pages = reader.pages[1:]
	return page, nil
}

func TestCreateConversationAPIUsesHeaderCustomer(t *testing.T) {
	creator := &fakeConversationCreator{
		result: application.CreateConversationResult{
			ID:              "conv_1",
			KnowledgeBaseID: "kb_1",
			Status:          domain.ConversationStatusAIActive,
		},
	}
	router := newChatTestRouter(creator, nil, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/conversations",
		bytes.NewBufferString(`{"knowledgeBaseId":"kb_1"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(customerIDHeader, "customer-1")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if creator.request.CustomerID != "customer-1" ||
		creator.request.KnowledgeBaseID != "kb_1" {
		t.Fatalf("unexpected request: %#v", creator.request)
	}
}

func TestSendMessageAPI(t *testing.T) {
	sender := &fakeMessageSender{
		result: application.Result{
			MessageID: "msg_1",
			RunID:     "run_1",
			RunStatus: domain.RunStatusPending,
		},
	}
	router := newChatTestRouter(nil, sender, nil)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/conversations/conv_1/messages",
		bytes.NewBufferString(`{"clientMessageId":"client-1","content":"如何重置密码？"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(customerIDHeader, "customer-1")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if sender.request.CustomerID != "customer-1" ||
		sender.request.ConversationID != "conv_1" ||
		sender.request.ClientMessageID != "client-1" {
		t.Fatalf("unexpected request: %#v", sender.request)
	}
	var payload sendMessageResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.RunID != "run_1" || payload.RunStatus != "pending" {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestGetMessageHistoryAPIUsesCustomerScopeAndReturnsRunResult(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	reader := &fakeMessageHistoryReader{page: domain.MessageHistoryPage{
		ConversationStatus: domain.ConversationStatusAIActive,
		Items: []domain.MessageHistoryItem{
			{
				Message: domain.Message{
					ID:             "message-assistant",
					ConversationID: "conversation-1",
					AgentRunID:     "run-1",
					Role:           domain.MessageRoleAssistant,
					Content:        "请在设置页面重置密码。[S1]",
					CreatedAt:      now,
				},
				RunID:     "run-1",
				RunStatus: domain.RunStatusCompleted,
				RunResult: map[string]any{
					"assessment":        map[string]any{"decision": "answerable"},
					"citations":         []any{map[string]any{"sourceId": "S1"}},
					"nextAction":        "confirm_ticket",
					"ticketDraft":       map[string]any{"title": "登录失败", "description": "无法登录", "priority": "high"},
					"approvalId":        "approval-1",
					"approvalExpiresAt": now.Add(time.Hour).Format(time.RFC3339Nano),
					"nodePath":          []any{"validate_input", "grounded_generate"},
					"toolCalls":         []any{map[string]any{"name": "draft_ticket"}},
				},
			},
		},
		NextBeforeMessageID: "message-cursor",
	}}
	router := newChatHistoryTestRouter(reader)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/conversations/conversation-1/messages?before=message-2&limit=20",
		nil,
	)
	request.Header.Set(customerIDHeader, "customer-1")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if reader.request.CustomerID != "customer-1" ||
		reader.request.ConversationID != "conversation-1" ||
		reader.request.BeforeMessageID != "message-2" ||
		reader.request.Limit != 20 {
		t.Fatalf("unexpected history request: %#v", reader.request)
	}
	var payload messageHistoryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) != 1 ||
		payload.Items[0].RunID != "run-1" ||
		payload.Items[0].Result["assessment"] == nil ||
		payload.Items[0].Result["approvalId"] != "approval-1" ||
		payload.Items[0].Result["ticketDraft"] == nil ||
		payload.Items[0].Result["nodePath"] != nil ||
		payload.Items[0].Result["toolCalls"] != nil ||
		payload.NextBeforeMessageID != "message-cursor" {
		t.Fatalf("unexpected history response: %#v", payload)
	}
}

func TestGetMessageHistoryAPIRejectsInvalidLimitBeforeUseCase(t *testing.T) {
	reader := &fakeMessageHistoryReader{}
	router := newChatHistoryTestRouter(reader)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/conversations/conversation-1/messages?limit=0",
		nil,
	)
	request.Header.Set(customerIDHeader, "customer-1")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if reader.request.ConversationID != "" {
		t.Fatalf("use case was called with invalid limit: %#v", reader.request)
	}
}

func TestRunEventsSSESupportsLastEventID(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	reader := &fakeRunEventReader{
		pages: []domain.RunEventPage{
			{
				RunID:  "run_1",
				Status: domain.RunStatusCompleted,
				Events: []domain.RunEvent{
					{
						ID:        "event_2",
						RunID:     "run_1",
						Sequence:  2,
						Type:      domain.EventTypeRunCompleted,
						Payload:   map[string]any{"status": "completed"},
						CreatedAt: now,
					},
				},
			},
		},
	}
	router := newChatTestRouter(nil, nil, reader)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/agent-runs/run_1/events",
		nil,
	)
	request.Header.Set(customerIDHeader, "customer-1")
	request.Header.Set("Last-Event-ID", "1")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	for _, expected := range []string{
		"id: 2",
		"event: run.completed",
		`"eventId":"event_2"`,
		`"runId":"run_1"`,
		`"sequence":2`,
		`"createdAt":"2026-07-25T12:00:00Z"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE body missing %q: %s", expected, body)
		}
	}
	if len(reader.requests) != 1 ||
		reader.requests[0].CustomerID != "customer-1" ||
		reader.requests[0].AfterSequence != 1 {
		t.Fatalf("unexpected event request: %#v", reader.requests)
	}
}

func TestRunEventsRejectsInvalidLastEventID(t *testing.T) {
	router := newChatTestRouter(nil, nil, &fakeRunEventReader{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/agent-runs/run_1/events",
		nil,
	)
	request.Header.Set(customerIDHeader, "customer-1")
	request.Header.Set("Last-Event-ID", "not-a-sequence")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}

func TestGetRunTraceAPI(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	reader := &fakeRunTraceReader{
		trace: domain.RunTraceSnapshot{
			RunID:          "run_1",
			RequestID:      "request_1",
			ConversationID: "conversation_1",
			Status:         domain.RunStatusCompleted,
			Result:         map[string]any{"answer": "知识回答"},
			CreatedAt:      now,
			StartedAt:      &now,
			CompletedAt:    &now,
			Events: []domain.RunEvent{
				{
					ID:        "event_approval_required",
					RunID:     "run_1",
					Sequence:  2,
					Type:      domain.EventTypeApprovalRequired,
					Payload:   map[string]any{"approvalId": "approval_1"},
					CreatedAt: now,
				},
			},
			Steps: []domain.RunStep{
				{
					Order: 1,
					RunStepDraft: domain.RunStepDraft{
						Name:             "grounded_generate",
						Component:        "ChatModel",
						ComponentType:    "deepseek/deepseek-v4-flash",
						Status:           "completed",
						StartedAt:        now,
						CompletedAt:      now,
						PromptTokens:     120,
						CompletionTokens: 30,
					},
				},
			},
		},
	}
	router := newKnowledgeTestRouter(nil, nil)
	engine := router.(*gin.Engine)
	registerRunTraceRoute(engine, reader)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/agent-runs/run_1",
		nil,
	)
	request.Header.Set(adminIDHeader, "admin-demo")

	engine.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	var payload runTraceResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if reader.runID != "run_1" ||
		payload.RequestID != "request_1" ||
		len(payload.Steps) != 1 ||
		len(payload.Events) != 1 ||
		payload.Events[0].Type != string(domain.EventTypeApprovalRequired) ||
		payload.Events[0].Payload["approvalId"] != "approval_1" ||
		payload.Steps[0].PromptTokens != 120 {
		t.Fatalf("unexpected Trace response: %#v", payload)
	}
}

func newChatTestRouter(
	conversationCreator ConversationCreator,
	messageSender MessageSender,
	eventReader RunEventReader,
) http.Handler {
	router := newKnowledgeTestRouter(nil, nil)
	engine, ok := router.(*gin.Engine)
	if !ok {
		panic("test router is not a Gin engine")
	}
	registerChatRoutes(engine, conversationCreator, messageSender, nil, eventReader)
	return engine
}

func newChatHistoryTestRouter(historyReader MessageHistoryReader) http.Handler {
	router := newKnowledgeTestRouter(nil, nil)
	engine, ok := router.(*gin.Engine)
	if !ok {
		panic("test router is not a Gin engine")
	}
	registerChatRoutes(engine, nil, nil, historyReader, nil)
	return engine
}
