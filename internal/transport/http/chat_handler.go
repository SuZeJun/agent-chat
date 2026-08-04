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

// MessageHistoryReader 定义客户历史消息 Handler 依赖的分页用例。
type MessageHistoryReader interface {
	ReadMessageHistory(
		context.Context,
		application.MessageHistoryRequest,
	) (domain.MessageHistoryPage, error)
}

// RunEventReader 定义 SSE Handler 依赖的增量事件用例。
type RunEventReader interface {
	ReadEvents(context.Context, application.EventRequest) (domain.RunEventPage, error)
}

// RunTraceReader 定义管理员 Run 详情 Handler 依赖的用例。
type RunTraceReader interface {
	GetRunTrace(context.Context, string) (domain.RunTraceSnapshot, error)
}

// createConversationRequest 指定会话服务端绑定的知识库。
type createConversationRequest struct {
	KnowledgeBaseID string `json:"knowledgeBaseId"`
}

// createConversationResponse 返回客户后续发送消息所需的会话标识。
type createConversationResponse struct {
	ID              string `json:"id"`
	KnowledgeBaseID string `json:"knowledgeBaseId"`
	Status          string `json:"status"`
}

// sendMessageRequest 使用客户端消息 ID 保证重复提交幂等。
type sendMessageRequest struct {
	ClientMessageID string `json:"clientMessageId"`
	Content         string `json:"content"`
}

// sendMessageResponse 返回已持久化消息和异步 Agent Run 的关联标识。
type sendMessageResponse struct {
	MessageID string `json:"messageId"`
	RunID     string `json:"runId"`
	RunStatus string `json:"runStatus"`
	Duplicate bool   `json:"duplicate"`
}

type messageHistoryResponse struct {
	Items               []messageHistoryItemResponse `json:"items"`
	NextBeforeMessageID string                       `json:"nextBeforeMessageId,omitempty"`
}

type messageHistoryItemResponse struct {
	ID        string         `json:"id"`
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	RunID     string         `json:"runId,omitempty"`
	RunStatus string         `json:"runStatus,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
	ErrorCode string         `json:"errorCode,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

// runEventResponse 是 SSE data 字段中的可去重事件结构。
type runEventResponse struct {
	EventID   string         `json:"eventId"`
	RunID     string         `json:"runId"`
	Sequence  int            `json:"sequence"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"createdAt"`
}

// runTraceResponse 是管理员可见的脱敏 Run 结果和 Trace。
type runTraceResponse struct {
	RunID          string                 `json:"runId"`
	RequestID      string                 `json:"requestId"`
	ConversationID string                 `json:"conversationId"`
	Question       string                 `json:"question"`
	Status         string                 `json:"status"`
	Result         map[string]any         `json:"result"`
	ErrorCode      string                 `json:"errorCode,omitempty"`
	Steps          []runTraceStepResponse `json:"steps"`
	Events         []runEventResponse     `json:"events"`
	CreatedAt      string                 `json:"createdAt"`
	StartedAt      string                 `json:"startedAt,omitempty"`
	CompletedAt    string                 `json:"completedAt,omitempty"`
}

// runTraceStepResponse 仅暴露节点身份、耗时和 Token，不包含 Prompt 或 Provider 正文。
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

// registerChatRoutes 组装客户会话、消息和 SSE 路由。
func registerChatRoutes(
	router *gin.Engine,
	conversationCreator ConversationCreator,
	messageSender MessageSender,
	historyReader MessageHistoryReader,
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
	if historyReader != nil {
		api.GET(
			"/conversations/:conversationId/messages",
			getMessageHistoryHandler(historyReader),
		)
	}
	if eventReader != nil {
		api.GET("/agent-runs/:runId/events", streamRunEventsHandler(eventReader))
	}
}

func getMessageHistoryHandler(service MessageHistoryReader) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(
			ctx,
			customerIDHeader,
			"customer_auth_required",
		)
		if !ok {
			return
		}
		limit := 0
		if rawLimit := strings.TrimSpace(ctx.Query("limit")); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err != nil || parsed <= 0 {
				writeAPIError(ctx, http.StatusBadRequest, "invalid_message_history_request", "history limit is invalid")
				return
			}
			limit = parsed
		}
		page, err := service.ReadMessageHistory(
			ctx.Request.Context(),
			application.MessageHistoryRequest{
				CustomerID:      customerID,
				ConversationID:  ctx.Param("conversationId"),
				BeforeMessageID: ctx.Query("before"),
				Limit:           limit,
			},
		)
		if err != nil {
			writeChatError(ctx, err)
			return
		}
		items := make([]messageHistoryItemResponse, len(page.Items))
		for index, item := range page.Items {
			items[index] = messageHistoryItemResponse{
				ID:        item.Message.ID,
				Role:      string(item.Message.Role),
				Content:   item.Message.Content,
				RunID:     item.RunID,
				RunStatus: string(item.RunStatus),
				Result:    publicMessageHistoryResult(item.RunResult),
				ErrorCode: item.RunErrorCode,
				CreatedAt: item.Message.CreatedAt.UTC().Format(time.RFC3339Nano),
			}
		}
		ctx.JSON(http.StatusOK, messageHistoryResponse{
			Items:               items,
			NextBeforeMessageID: page.NextBeforeMessageID,
		})
	}
}

// publicMessageHistoryResult 只暴露客户恢复界面所需字段，不返回节点路径、工具调用等内部 Trace。
func publicMessageHistoryResult(result map[string]any) map[string]any {
	if len(result) == 0 {
		return nil
	}
	public := make(map[string]any, 6)
	// 工单草稿、审批 ID 与过期时间是刷新后恢复确认界面的必要契约；它们只描述
	// 当前客户可见且即将执行的操作，不包含工具原始输出或内部节点信息。
	for _, field := range []string{
		"assessment",
		"citations",
		"nextAction",
		"ticketDraft",
		"approvalId",
		"approvalExpiresAt",
	} {
		if value, exists := result[field]; exists {
			public[field] = value
		}
	}
	if len(public) == 0 {
		return nil
	}
	return public
}

// registerRunTraceRoute 单独注册管理员 Trace，避免与客户资源权限混淆。
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

// streamRunEventsHandler 先读取持久化事件，再按 Last-Event-ID 增量轮询直到 Run 终态。
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
		events := make([]runEventResponse, len(trace.Events))
		for index, event := range trace.Events {
			events[index] = newRunEventResponse(event)
		}
		response := runTraceResponse{
			RunID:          trace.RunID,
			RequestID:      trace.RequestID,
			ConversationID: trace.ConversationID,
			Question:       trace.Question,
			Status:         string(trace.Status),
			Result:         trace.Result,
			ErrorCode:      trace.ErrorCode,
			Steps:          steps,
			Events:         events,
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

// writeRunEvent 使用持久化 sequence 作为 SSE id，支持客户端去重和断线续传。
func writeRunEvent(ctx *gin.Context, event domain.RunEvent) error {
	payload, err := json.Marshal(newRunEventResponse(event))
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

func newRunEventResponse(event domain.RunEvent) runEventResponse {
	return runEventResponse{
		EventID:   event.ID,
		RunID:     event.RunID,
		Sequence:  event.Sequence,
		Type:      string(event.Type),
		Payload:   event.Payload,
		CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// parseLastEventID 将客户端游标限制为非负 sequence，空值表示从头读取。
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
		"invalid_message_history_request",
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
