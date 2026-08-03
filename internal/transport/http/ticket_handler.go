package httptransport

import (
	"context"
	"errors"
	"net/http"

	ticketapp "agent-chat/internal/application/ticket"

	"github.com/gin-gonic/gin"
)

// TicketApprover 定义确认与取消工单草稿所需的最小用例能力。
type TicketApprover interface {
	Confirm(ctx context.Context, customerID string, approvalID string) (ticketapp.Decision, error)
	Cancel(ctx context.Context, customerID string, approvalID string) (ticketapp.Decision, error)
	Get(ctx context.Context, customerID string, approvalID string) (ticketapp.Decision, error)
}

// ticketApprovalResponse 是审批决策的客户端视图。
//
// 只在工单确实创建后才包含 ticket：取消与过期路径必须让客户端明确看到没有工单，
// 而不是收到一个空对象后自行推断。
type ticketApprovalResponse struct {
	ApprovalID      string          `json:"approvalId"`
	Status          string          `json:"status"`
	Draft           ticketDraftBody `json:"draft"`
	Ticket          *ticketBody     `json:"ticket,omitempty"`
	ExecutionStatus string          `json:"executionStatus"`
}

type ticketDraftBody struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
}

type ticketBody struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

// registerTicketRoutes 组装客户端确认与取消工单草稿的路由。
//
// 路径以审批为资源而非以会话为资源：审批 ID 已经唯一，且服务端按客户归属校验
// 访问权限，附带会话 ID 只会多一个可被伪造却不被信任的参数。
func registerTicketRoutes(router *gin.Engine, approver TicketApprover) {
	if approver == nil {
		return
	}
	api := router.Group("/api/v1")
	api.GET("/ticket-approvals/:approvalId", getTicketApprovalHandler(approver))
	api.POST("/ticket-approvals/:approvalId/confirm", confirmTicketHandler(approver))
	api.POST("/ticket-approvals/:approvalId/cancel", cancelTicketHandler(approver))
}

func confirmTicketHandler(approver TicketApprover) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(ctx, customerIDHeader, "customer_required")
		if !ok {
			return
		}
		decision, err := approver.Confirm(ctx.Request.Context(), customerID, ctx.Param("approvalId"))
		if err != nil {
			writeTicketError(ctx, err)
			return
		}
		status := http.StatusAccepted
		if decision.Ticket != nil {
			status = http.StatusOK
		}
		ctx.JSON(status, newTicketApprovalResponse(decision))
	}
}

func cancelTicketHandler(approver TicketApprover) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(ctx, customerIDHeader, "customer_required")
		if !ok {
			return
		}
		decision, err := approver.Cancel(ctx.Request.Context(), customerID, ctx.Param("approvalId"))
		if err != nil {
			writeTicketError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, newTicketApprovalResponse(decision))
	}
}

func getTicketApprovalHandler(approver TicketApprover) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		customerID, ok := requireHeaderIdentity(ctx, customerIDHeader, "customer_required")
		if !ok {
			return
		}
		decision, err := approver.Get(
			ctx.Request.Context(),
			customerID,
			ctx.Param("approvalId"),
		)
		if err != nil {
			writeTicketError(ctx, err)
			return
		}
		ctx.JSON(http.StatusOK, newTicketApprovalResponse(decision))
	}
}

func newTicketApprovalResponse(decision ticketapp.Decision) ticketApprovalResponse {
	response := ticketApprovalResponse{
		ApprovalID: decision.Approval.ID,
		Status:     string(decision.Approval.Status),
		Draft: ticketDraftBody{
			Title:       decision.Approval.Draft.Title,
			Description: decision.Approval.Draft.Description,
			Priority:    string(decision.Approval.Draft.Priority),
		},
		ExecutionStatus: "awaiting_confirmation",
	}
	if decision.Approval.Status == "approved" {
		response.ExecutionStatus = "pending"
	} else if decision.Approval.Status != "pending" {
		response.ExecutionStatus = string(decision.Approval.Status)
	}
	if decision.Ticket != nil {
		response.Ticket = &ticketBody{
			ID:     decision.Ticket.ID,
			Number: decision.Ticket.Number,
		}
		response.ExecutionStatus = "succeeded"
	}
	return response
}

// writeTicketError 映射审批失败为稳定状态码。
//
// 过期单独用 410 Gone：客户端据此提示重新发起，而 409 会被误解为可以重试。
func writeTicketError(ctx *gin.Context, err error) {
	var failure *ticketapp.Failure
	if !errors.As(err, &failure) {
		writeAPIError(ctx, http.StatusInternalServerError, "internal_error", "request failed")
		return
	}
	switch failure.Code {
	case "invalid_approval_scope", "invalid_ticket_command":
		writeAPIError(ctx, http.StatusBadRequest, failure.Code, "ticket request is invalid")
	case "ticket_approval_not_found":
		writeAPIError(ctx, http.StatusNotFound, failure.Code, "ticket approval was not found")
	case "ticket_approval_expired":
		writeAPIError(ctx, http.StatusGone, failure.Code, "ticket approval has expired")
	case "ticket_approval_not_actionable":
		writeAPIError(ctx, http.StatusConflict, failure.Code, "ticket approval is no longer actionable")
	default:
		writeAPIError(
			ctx,
			http.StatusServiceUnavailable,
			failure.Code,
			"ticket service is temporarily unavailable",
		)
	}
}
