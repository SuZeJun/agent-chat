package httptransport

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"

	"github.com/gin-gonic/gin"
)

// HandoffService 定义客户转人工与客服工作台 Handler 依赖的用例。
type HandoffService interface {
	RequestHandoff(context.Context, string, string, string) (domain.HandoffConversation, error)
	SendCustomerMessage(context.Context, application.Request) (domain.Message, bool, error)
	ListQueue(context.Context, string) ([]domain.HandoffConversation, error)
	GetConversation(context.Context, string, string) (domain.HandoffConversation, error)
	Takeover(context.Context, string, string) (domain.HandoffConversation, error)
	SendAgentMessage(context.Context, string, string, string) (domain.Message, error)
	ResumeAI(context.Context, string, string) (domain.HandoffConversation, error)
	ReadCustomerEvents(context.Context, string, string, int) (domain.ConversationEventPage, error)
	ReadAgentEvents(context.Context, string, string, int) (domain.ConversationEventPage, error)
}

type handoffRequest struct {
	Reason string `json:"reason"`
}

type agentMessageRequest struct {
	Content string `json:"content"`
}

type handoffSummaryResponse struct {
	CustomerRequest     string                   `json:"customerRequest"`
	ConfirmedFacts      []string                 `json:"confirmedFacts"`
	UnresolvedQuestions []string                 `json:"unresolvedQuestions"`
	RiskSignals         []string                 `json:"riskSignals"`
	Citations           []domain.HandoffCitation `json:"citations"`
	ToolCalls           []domain.HandoffToolCall `json:"toolCalls"`
	RecommendedAction   string                   `json:"recommendedAction"`
	CreatedAt           string                   `json:"createdAt"`
	UpdatedAt           string                   `json:"updatedAt"`
}

type handoffMessageResponse struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

type handoffConversationResponse struct {
	ID              string                      `json:"id"`
	CustomerID      string                      `json:"customerId"`
	KnowledgeBaseID string                      `json:"knowledgeBaseId"`
	Status          string                      `json:"status"`
	AssignedAgentID string                      `json:"assignedAgentId,omitempty"`
	LastMessageAt   string                      `json:"lastMessageAt,omitempty"`
	Summary         handoffSummaryResponse      `json:"summary"`
	Messages        []handoffMessageResponse    `json:"messages,omitempty"`
	Events          []conversationEventResponse `json:"events,omitempty"`
}

type customerHandoffResponse struct {
	ConversationID string `json:"conversationId"`
	Status         string `json:"status"`
}

type conversationEventResponse struct {
	EventID        string         `json:"eventId"`
	ConversationID string         `json:"conversationId"`
	Sequence       int            `json:"sequence"`
	Type           string         `json:"type"`
	ActorType      string         `json:"actorType"`
	ActorID        string         `json:"actorId,omitempty"`
	Payload        map[string]any `json:"payload"`
	CreatedAt      string         `json:"createdAt"`
}

type conversationEventPageResponse struct {
	ConversationID  string                      `json:"conversationId"`
	Status          string                      `json:"status"`
	AssignedAgentID string                      `json:"assignedAgentId,omitempty"`
	Items           []conversationEventResponse `json:"items"`
}

func registerHandoffRoutes(router *gin.Engine, service HandoffService) {
	if service == nil {
		return
	}
	router.POST("/api/v1/conversations/:conversationId/handoff", requestHandoffHandler(service))
	router.POST("/api/v1/conversations/:conversationId/handoff/messages", customerHandoffMessageHandler(service))
	router.GET("/api/v1/conversations/:conversationId/events", customerConversationEventsHandler(service))

	agent := router.Group("/api/v1/agent/conversations")
	agent.GET("", listHandoffQueueHandler(service))
	agent.GET("/:conversationId", getHandoffConversationHandler(service))
	agent.POST("/:conversationId/takeover", takeoverHandoffHandler(service))
	agent.POST("/:conversationId/messages", agentHandoffMessageHandler(service))
	agent.POST("/:conversationId/resume-ai", resumeAIHandler(service))
	agent.GET("/:conversationId/events", agentConversationEventsHandler(service))
}

func requestHandoffHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(ctx, customerIDHeader, "customer_auth_required")
		if !ok {
			return
		}
		var request handoffRequest
		if err := decodeJSONBody(ctx, &request); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		result, err := service.RequestHandoff(ctx.Request.Context(), customerID, ctx.Param("conversationId"), request.Reason)
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		ctx.JSON(http.StatusAccepted, customerHandoffResponse{
			ConversationID: result.Conversation.ID, Status: string(result.Conversation.Status),
		})
	}
}

func customerHandoffMessageHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(ctx, customerIDHeader, "customer_auth_required")
		if !ok {
			return
		}
		var request sendMessageRequest
		if err := decodeJSONBody(ctx, &request); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		message, duplicate, err := service.SendCustomerMessage(ctx.Request.Context(), application.Request{
			RequestID: ctx.GetString("request_id"), CustomerID: customerID,
			ConversationID: ctx.Param("conversationId"), ClientMessageID: request.ClientMessageID, Content: request.Content,
		})
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		response := mapHandoffMessage(message)
		response.Duplicate = duplicate
		status := http.StatusCreated
		if duplicate {
			status = http.StatusOK
		}
		ctx.JSON(status, response)
	}
}

func listHandoffQueueHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		agentID, ok := requireHeaderIdentity(ctx, agentIDHeader, "agent_auth_required")
		if !ok {
			return
		}
		items, err := service.ListQueue(ctx.Request.Context(), agentID)
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		response := make([]handoffConversationResponse, len(items))
		for index := range items {
			response[index] = mapHandoffConversation(items[index], false)
		}
		ctx.JSON(http.StatusOK, gin.H{"items": response})
	}
}

func getHandoffConversationHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		agentID, ok := requireHeaderIdentity(ctx, agentIDHeader, "agent_auth_required")
		if !ok {
			return
		}
		result, err := service.GetConversation(ctx.Request.Context(), agentID, ctx.Param("conversationId"))
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, mapHandoffConversation(result, true))
	}
}

func takeoverHandoffHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		agentID, ok := requireHeaderIdentity(ctx, agentIDHeader, "agent_auth_required")
		if !ok {
			return
		}
		result, err := service.Takeover(ctx.Request.Context(), agentID, ctx.Param("conversationId"))
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, mapHandoffConversation(result, true))
	}
}

func agentHandoffMessageHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		agentID, ok := requireHeaderIdentity(ctx, agentIDHeader, "agent_auth_required")
		if !ok {
			return
		}
		var request agentMessageRequest
		if err := decodeJSONBody(ctx, &request); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		message, err := service.SendAgentMessage(ctx.Request.Context(), agentID, ctx.Param("conversationId"), request.Content)
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		ctx.JSON(http.StatusCreated, mapHandoffMessage(message))
	}
}

func resumeAIHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		agentID, ok := requireHeaderIdentity(ctx, agentIDHeader, "agent_auth_required")
		if !ok {
			return
		}
		result, err := service.ResumeAI(ctx.Request.Context(), agentID, ctx.Param("conversationId"))
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, mapHandoffConversation(result, true))
	}
}

func customerConversationEventsHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(ctx, customerIDHeader, "customer_auth_required")
		if !ok {
			return
		}
		after, ok := parseConversationEventCursor(ctx)
		if !ok {
			return
		}
		page, err := service.ReadCustomerEvents(ctx.Request.Context(), customerID, ctx.Param("conversationId"), after)
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, mapConversationEventPage(page, false))
	}
}

func agentConversationEventsHandler(service HandoffService) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		agentID, ok := requireHeaderIdentity(ctx, agentIDHeader, "agent_auth_required")
		if !ok {
			return
		}
		after, ok := parseConversationEventCursor(ctx)
		if !ok {
			return
		}
		page, err := service.ReadAgentEvents(ctx.Request.Context(), agentID, ctx.Param("conversationId"), after)
		if err != nil {
			writeHandoffError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, mapConversationEventPage(page, true))
	}
}

func parseConversationEventCursor(ctx *gin.Context) (int, bool) {
	raw := strings.TrimSpace(ctx.Query("after"))
	if raw == "" {
		return 0, true
	}
	after, err := strconv.Atoi(raw)
	if err != nil || after < 0 {
		writeAPIError(ctx, http.StatusBadRequest, "invalid_conversation_event_request", "event cursor is invalid")
		return 0, false
	}
	return after, true
}

func mapHandoffConversation(item domain.HandoffConversation, includeDetails bool) handoffConversationResponse {
	response := handoffConversationResponse{
		ID: item.Conversation.ID, CustomerID: item.Conversation.CustomerID,
		KnowledgeBaseID: item.Conversation.KnowledgeBaseID, Status: string(item.Conversation.Status),
		AssignedAgentID: item.AssignedAgentID, Summary: mapHandoffSummary(item.Summary),
	}
	if item.LastMessageAt != nil {
		response.LastMessageAt = item.LastMessageAt.UTC().Format(time.RFC3339Nano)
	}
	if includeDetails {
		response.Messages = make([]handoffMessageResponse, len(item.Messages))
		for index := range item.Messages {
			response.Messages[index] = mapHandoffMessage(item.Messages[index])
		}
		response.Events = make([]conversationEventResponse, len(item.Events))
		for index := range item.Events {
			response.Events[index] = mapConversationEvent(item.Events[index], true)
		}
	}
	return response
}

func mapHandoffSummary(summary domain.HandoffSummary) handoffSummaryResponse {
	return handoffSummaryResponse{
		CustomerRequest: summary.CustomerRequest, ConfirmedFacts: summary.ConfirmedFacts,
		UnresolvedQuestions: summary.UnresolvedQuestions, RiskSignals: summary.RiskSignals,
		Citations: summary.Citations, ToolCalls: summary.ToolCalls,
		RecommendedAction: summary.RecommendedAction,
		CreatedAt:         summary.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: summary.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapHandoffMessage(message domain.Message) handoffMessageResponse {
	return handoffMessageResponse{ID: message.ID, Role: string(message.Role), Content: message.Content, CreatedAt: message.CreatedAt.UTC().Format(time.RFC3339Nano)}
}

func mapConversationEventPage(page domain.ConversationEventPage, includeActorID bool) conversationEventPageResponse {
	items := make([]conversationEventResponse, len(page.Events))
	for index := range page.Events {
		items[index] = mapConversationEvent(page.Events[index], includeActorID)
	}
	response := conversationEventPageResponse{ConversationID: page.ConversationID, Status: string(page.Status), Items: items}
	if includeActorID {
		response.AssignedAgentID = page.AssignedAgentID
	}
	return response
}

func mapConversationEvent(event domain.ConversationEvent, includeActorID bool) conversationEventResponse {
	response := conversationEventResponse{
		EventID: event.ID, ConversationID: event.ConversationID, Sequence: event.Sequence,
		Type: string(event.Type), ActorType: string(event.ActorType), Payload: event.Payload,
		CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if includeActorID {
		response.ActorID = event.ActorID
	}
	return response
}

func writeHandoffError(ctx *gin.Context, err error) {
	var failure *application.Failure
	if !errors.As(err, &failure) {
		writeAPIError(ctx, http.StatusInternalServerError, "internal_error", "request failed")
		return
	}
	switch failure.Code {
	case "invalid_handoff_request", "invalid_handoff_message", "invalid_handoff_queue_request",
		"invalid_handoff_conversation_request", "invalid_handoff_takeover", "invalid_agent_message",
		"invalid_resume_ai_request", "invalid_conversation_event_request":
		writeAPIError(ctx, http.StatusBadRequest, failure.Code, "handoff request is invalid")
	case "handoff_conversation_not_found":
		writeAPIError(ctx, http.StatusNotFound, failure.Code, "handoff conversation was not found")
	case "handoff_state_conflict", "handoff_message_id_conflict":
		writeAPIError(ctx, http.StatusConflict, failure.Code, "handoff state conflicts with the request")
	default:
		writeAPIError(ctx, http.StatusServiceUnavailable, failure.Code, "handoff service is temporarily unavailable")
	}
}
