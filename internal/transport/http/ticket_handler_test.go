package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ticketapp "agent-chat/internal/application/ticket"
	domain "agent-chat/internal/domain/ticket"

	"github.com/gin-gonic/gin"
)

type fakeTicketApprover struct {
	confirmDecision ticketapp.Decision
	confirmErr      error
	getDecision     ticketapp.Decision
	customerID      string
	approvalID      string
}

func (approver *fakeTicketApprover) Confirm(
	_ context.Context,
	customerID string,
	approvalID string,
) (ticketapp.Decision, error) {
	approver.customerID = customerID
	approver.approvalID = approvalID
	return approver.confirmDecision, approver.confirmErr
}
func (approver *fakeTicketApprover) Cancel(
	context.Context,
	string,
	string,
) (ticketapp.Decision, error) {
	return approver.confirmDecision, nil
}
func (approver *fakeTicketApprover) Get(
	context.Context,
	string,
	string,
) (ticketapp.Decision, error) {
	return approver.getDecision, nil
}

func handlerApproval(status domain.ApprovalStatus) domain.Approval {
	now := time.Now().UTC()
	return domain.Approval{
		ID:             "approval-1",
		ConversationID: "conversation-1",
		CustomerID:     "customer-1",
		AgentRunID:     "run-1",
		Draft: domain.Draft{
			Title:       "无法导出账单",
			Description: "点击导出没有反应。",
			Priority:    domain.PriorityHigh,
		},
		Status:         status,
		IdempotencyKey: domain.DeriveIdempotencyKey("run-1"),
		CreatedAt:      now.Add(-time.Minute),
		ExpiresAt:      now.Add(time.Minute),
	}
}

func TestConfirmTicketReturnsAcceptedUntilWorkerCompletes(t *testing.T) {
	approver := &fakeTicketApprover{
		confirmDecision: ticketapp.Decision{
			Approval: handlerApproval(domain.ApprovalStatusApproved),
		},
	}
	router := gin.New()
	registerTicketRoutes(router, approver)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/ticket-approvals/approval-1/confirm",
		nil,
	)
	request.Header.Set(customerIDHeader, "customer-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	var body ticketApprovalResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ExecutionStatus != "pending" || body.Ticket != nil ||
		approver.customerID != "customer-1" || approver.approvalID != "approval-1" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestGetTicketApprovalReturnsCompletedNumber(t *testing.T) {
	approval := handlerApproval(domain.ApprovalStatusApproved)
	approver := &fakeTicketApprover{
		getDecision: ticketapp.Decision{
			Approval: approval,
			Ticket: &domain.Ticket{
				ID:             "ticket-1",
				Number:         "TK-1",
				ConversationID: approval.ConversationID,
				CustomerID:     approval.CustomerID,
				ApprovalID:     approval.ID,
				Draft:          approval.Draft,
				CreatedAt:      time.Now().UTC(),
			},
		},
	}
	router := gin.New()
	registerTicketRoutes(router, approver)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ticket-approvals/approval-1", nil)
	request.Header.Set(customerIDHeader, "customer-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	var body ticketApprovalResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ExecutionStatus != "succeeded" || body.Ticket == nil || body.Ticket.Number != "TK-1" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestConfirmTicketMapsExpiredToGone(t *testing.T) {
	approver := &fakeTicketApprover{
		confirmErr: &ticketapp.Failure{Code: "ticket_approval_expired"},
	}
	router := gin.New()
	registerTicketRoutes(router, approver)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/ticket-approvals/approval-1/confirm",
		nil,
	)
	request.Header.Set(customerIDHeader, "customer-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusGone {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
}
