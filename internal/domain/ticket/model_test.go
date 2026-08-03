package ticket

import (
	"testing"
	"time"
)

func TestApprovalStatusTerminal(t *testing.T) {
	if ApprovalStatusPending.Terminal() {
		t.Fatal("pending approval must remain actionable")
	}
	for _, status := range []ApprovalStatus{
		ApprovalStatusApproved,
		ApprovalStatusCancelled,
		ApprovalStatusExpired,
	} {
		if !status.Terminal() {
			t.Fatalf("status %q must be terminal", status)
		}
	}
}

func TestTicketJobCommandsValidateStableIdentities(t *testing.T) {
	now := time.Now().UTC()
	command := ConfirmCommand{
		CustomerID:    "customer-1",
		ApprovalID:    "approval-1",
		JobID:         "job-1",
		TicketID:      "ticket-1",
		TicketNumber:  "TK-1",
		EventID:       "event-confirmed",
		TicketEventID: "event-created",
		OccurredAt:    now,
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("valid ConfirmCommand was rejected: %v", err)
	}
	command.TicketEventID = ""
	if err := command.Validate(); err == nil {
		t.Fatal("missing ticket event identity was accepted")
	}
}
