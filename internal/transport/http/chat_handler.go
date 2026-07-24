package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	application "agent-chat/internal/application/chat"
	domain "agent-chat/internal/domain/chat"

	"github.com/gin-gonic/gin"
)

const (
	runEventPollInterval = 500 * time.Millisecond
	runEventPageSize     = 100
)

// ConversationCreator 定义创建客户会话 Handler 依赖的用例。
type ConversationCreator interface {
	Create(
		context.Context,
		application.CreateConversationRequest,
	) (application.CreateConversationResult, error)
}

// MessageSender 定义客户发送消息 Handler 依赖的用例。
type MessageSender interface {
	SendMessage(context.Context, application.Request) (application.Result, error)
}

// RunEventReader 定义 SSE Handler 依赖的增量事件用例。
type RunEventReader interface {
	ReadEvents(context.Context, application.EventRequest) (domain.RunEventPage, error)
}

// RunTraceReader 定义管理员 Run 详情 Handler 依赖的用例。
type RunTraceReader interface {
	GetRunTrace(context.Context, string) (domain.RunTraceSnapshot, error)
}

type createConversationRequest struct {
	KnowledgeBaseID string `json:"knowledgeBaseId"`
}

type createConversationResponse struct {
	ID              string `json:"id"`
	KnowledgeBaseID string `json:"knowledgeBaseId"`
	Status          string `json:"status"`
}

type sendMessageRequest struct {
	ClientMessageID string `json:"clientMessageId"`
	Content         string `json:"content"`
}

type sendMessageResponse struct {
	MessageID string `json:"messageId"`
	RunID     string `json:"runId"`
	RunStatus string `json:"runStatus"`
	Duplicate bool   `json:"duplicate"`
}

type runEventResponse struct {
	EventID   string         `json:"eventId"`
	RunID     string         `json:"runId"`
	Sequence  int            `json:"sequence"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"createdAt"`
}

type runTraceResponse struct {
	RunID          string                 `json:"runId"`
	RequestID      string                 `json:"requestId"`
	ConversationID string                 `json:"conversationId"`
	Status         string                 `json:"status"`
	Result         map[string]any         `json:"result"`
	ErrorCode      string                 `json:"errorCode,omitempty"`
	Steps          []runTraceStepResponse `json:"steps"`
	CreatedAt      string                 `json:"createdAt"`
	StartedAt      string                 `json:"startedAt,omitempty"`
	CompletedAt    string                 `json:"completedAt,omitempty"`
}

type runTraceStepResponse struct {
	Order            int    `json:"order"`
	Name             string `json:"name"`
	Component        string `json:"component"`
	ComponentType    string `json:"componentType"`
	Status           string `json:"status"`
	DurationMillis   int64  `json:"durationMillis"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	StartedAt        string `json:"startedAt"`
	CompletedAt      string `json:"completedAt"`
}

func registerChatRoutes(
	router *gin.Engine,
	conversationCreator ConversationCreator,
	messageSender MessageSender,
	eventReader RunEventReader,
) {
	api := router.Group("/api/v1")
	if conversationCreator != nil {
		api.POST("/conversations", createConversationHandler(conversationCreator))
	}
	if messageSender != nil {
		api.POST(
			"/conversations/:conversationId/messages",
			sendMessageHandler(messageSender),
		)
	}
	if eventReader != nil {
		api.GET("/agent-runs/:runId/events", streamRunEventsHandler(eventReader))
	}
}

func registerRunTraceRoute(router *gin.Engine, traceReader RunTraceReader) {
	if traceReader == nil {
		return
	}
	router.GET("/api/v1/admin/agent-runs/:runId", getRunTraceHandler(traceReader))
}

func createConversationHandler(service ConversationCreator) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(
			ctx,
			customerIDHeader,
			"customer_auth_required",
		)
		if !ok {
			return
		}
		var request createConversationRequest
		if err := decodeJSONBody(ctx, &request); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		result, err := service.Create(
			ctx.Request.Context(),
			application.CreateConversationRequest{
				CustomerID:      customerID,
				KnowledgeBaseID: request.KnowledgeBaseID,
			},
		)
		if err != nil {
			writeChatError(ctx, err)
			return
		}
		ctx.JSON(http.StatusCreated, createConversationResponse{
			ID:              result.ID,
			KnowledgeBaseID: result.KnowledgeBaseID,
			Status:          string(result.Status),
		})
	}
}

func sendMessageHandler(service MessageSender) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(
			ctx,
			customerIDHeader,
			"customer_auth_required",
		)
		if !ok {
			return
		}
		var request sendMessageRequest
		if err := decodeJSONBody(ctx, &request); err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_request_body", "request body is invalid")
			return
		}
		result, err := service.SendMessage(ctx.Request.Context(), application.Request{
			RequestID:       ctx.GetString("request_id"),
			CustomerID:      customerID,
			ConversationID:  ctx.Param("conversationId"),
			ClientMessageID: request.ClientMessageID,
			Content:         request.Content,
		})
		if err != nil {
			writeChatError(ctx, err)
			return
		}
		status := http.StatusAccepted
		if result.Duplicate {
			status = http.StatusOK
		}
		ctx.JSON(status, sendMessageResponse{
			MessageID: result.MessageID,
			RunID:     result.RunID,
			RunStatus: string(result.RunStatus),
			Duplicate: result.Duplicate,
		})
	}
}

func streamRunEventsHandler(service RunEventReader) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(
			ctx,
			customerIDHeader,
			"customer_auth_required",
		)
		if !ok {
			return
		}
		afterSequence, err := parseLastEventID(ctx.GetHeader("Last-Event-ID"))
		if err != nil {
			writeAPIError(ctx, http.StatusBadRequest, "invalid_last_event_id", "Last-Event-ID is invalid")
			return
		}
		request := application.EventRequest{
			CustomerID:    customerID,
			RunID:         ctx.Param("runId"),
			AfterSequence: afterSequence,
		}
		page, err := service.ReadEvents(ctx.Request.Context(), request)
		if err != nil {
			writeChatError(ctx, err)
			return
		}

		ctx.Header("Content-Type", "text/event-stream; charset=utf-8")
		ctx.Header("Cache-Control", "no-cache")
		ctx.Header("Connection", "keep-alive")
		ctx.Header("X-Accel-Buffering", "no")
		ctx.Status(http.StatusOK)
		flusher, ok := ctx.Writer.(http.Flusher)
		if !ok {
			return
		}

		currentSequence := afterSequence
		for {
			for _, event := range page.Events {
				if err := writeRunEvent(ctx, event); err != nil {
					return
				}
				currentSequence = event.Sequence
				flusher.Flush()
			}
			if page.Terminal() && len(page.Events) < runEventPageSize {
				return
			}
			if len(page.Events) == runEventPageSize {
				request.AfterSequence = currentSequence
				page, err = service.ReadEvents(ctx.Request.Context(), request)
				if err != nil {
					return
				}
				continue
			}

			timer := time.NewTimer(runEventPollInterval)
			select {
			case <-ctx.Request.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			request.AfterSequence = currentSequence
			page, err = service.ReadEvents(ctx.Request.Context(), request)
			if err != nil {
				return
			}
		}
	}
}

func getRunTraceHandler(service RunTraceReader) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if _, ok := requireHeaderIdentity(ctx, adminIDHeader, "admin_auth_required"); !ok {
			return
		}
		trace, err := service.GetRunTrace(
			ctx.Request.Context(),
			ctx.Param("runId"),
		)
		if err != nil {
			writeChatError(ctx, err)
			return
		}
		steps := make([]runTraceStepResponse, len(trace.Steps))
		for index, step := range trace.Steps {
			steps[index] = runTraceStepResponse{
				Order:            step.Order,
				Name:             step.Name,
				Component:        step.Component,
				ComponentType:    step.ComponentType,
				Status:           step.Status,
				DurationMillis:   step.DurationMillis,
				PromptTokens:     step.PromptTokens,
				CompletionTokens: step.CompletionTokens,
				StartedAt:        step.StartedAt.UTC().Format(time.RFC3339Nano),
				CompletedAt:      step.CompletedAt.UTC().Format(time.RFC3339Nano),
			}
		}
		response := runTraceResponse{
			RunID:          trace.RunID,
			RequestID:      trace.RequestID,
			ConversationID: trace.ConversationID,
			Status:         string(trace.Status),
			Result:         trace.Result,
			ErrorCode:      trace.ErrorCode,
			Steps:          steps,
			CreatedAt:      trace.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if trace.StartedAt != nil {
			response.StartedAt = trace.StartedAt.UTC().Format(time.RFC3339Nano)
		}
		if trace.CompletedAt != nil {
			response.CompletedAt = trace.CompletedAt.UTC().Format(time.RFC3339Nano)
		}
		ctx.JSON(http.StatusOK, response)
	}
}

func writeRunEvent(ctx *gin.Context, event domain.RunEvent) error {
	payload, err := json.Marshal(runEventResponse{
		EventID:   event.ID,
		RunID:     event.RunID,
		Sequence:  event.Sequence,
		Type:      string(event.Type),
		Payload:   event.Payload,
		CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		ctx.Writer,
		"id: %d\nevent: %s\ndata: %s\n\n",
		event.Sequence,
		event.Type,
		payload,
	)
	return err
}

func parseLastEventID(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.Atoi(value)
	if err != nil || sequence < 0 {
		return 0, errors.New("Last-Event-ID must be a non-negative sequence")
	}
	return sequence, nil
}

func writeChatError(ctx *gin.Context, err error) {
	var failure *application.Failure
	if !errors.As(err, &failure) {
		writeAPIError(ctx, http.StatusInternalServerError, "internal_error", "request failed")
		return
	}
	switch failure.Code {
	case "invalid_create_conversation",
		"invalid_send_message",
		"invalid_run_event_request",
		"invalid_run_trace_request":
		writeAPIError(ctx, http.StatusBadRequest, failure.Code, "chat request is invalid")
	case "knowledge_base_not_found",
		"conversation_not_found",
		"agent_run_not_found":
		writeAPIError(ctx, http.StatusNotFound, failure.Code, "chat resource was not found")
	case "conversation_not_ai_active", "client_message_id_conflict":
		writeAPIError(ctx, http.StatusConflict, failure.Code, "chat state conflicts with the request")
	default:
		writeAPIError(ctx, http.StatusServiceUnavailable, failure.Code, "chat service is temporarily unavailable")
	}
}
