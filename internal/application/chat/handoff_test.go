package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domain "agent-chat/internal/domain/chat"
)

type fakeHandoffRepository struct {
	requestCommand  RequestHandoffCommand
	customerCommand CustomerHandoffMessageCommand
	takeoverCommand TakeoverHandoffCommand
	agentCommand    AgentHandoffMessageCommand
	resumeCommand   ResumeAICommand
	err             error
}

func (repository *fakeHandoffRepository) RequestHandoff(_ context.Context, command RequestHandoffCommand) (domain.HandoffConversation, error) {
	repository.requestCommand = command
	return domain.HandoffConversation{}, repository.err
}

func (repository *fakeHandoffRepository) SaveHandoffCustomerMessage(_ context.Context, command CustomerHandoffMessageCommand) (domain.Message, bool, error) {
	repository.customerCommand = command
	return command.Message, false, repository.err
}

func (*fakeHandoffRepository) ListHandoffConversations(context.Context, string) ([]domain.HandoffConversation, error) {
	return nil, nil
}

func (*fakeHandoffRepository) LoadHandoffConversation(context.Context, string, string) (domain.HandoffConversation, error) {
	return domain.HandoffConversation{}, nil
}

func (repository *fakeHandoffRepository) TakeoverHandoff(_ context.Context, command TakeoverHandoffCommand) (domain.HandoffConversation, error) {
	repository.takeoverCommand = command
	return domain.HandoffConversation{}, repository.err
}

func (repository *fakeHandoffRepository) SaveHandoffAgentMessage(_ context.Context, command AgentHandoffMessageCommand) (domain.Message, error) {
	repository.agentCommand = command
	return command.Message, repository.err
}

func (repository *fakeHandoffRepository) ResumeAI(_ context.Context, command ResumeAICommand) (domain.HandoffConversation, error) {
	repository.resumeCommand = command
	return domain.HandoffConversation{}, repository.err
}

func (*fakeHandoffRepository) LoadCustomerConversationEvents(context.Context, string, string, int, int) (domain.ConversationEventPage, error) {
	return domain.ConversationEventPage{}, nil
}

func (*fakeHandoffRepository) LoadAgentConversationEvents(context.Context, string, string, int, int) (domain.ConversationEventPage, error) {
	return domain.ConversationEventPage{}, nil
}

func TestHandoffServiceBuildsScopedCommands(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	repository := &fakeHandoffRepository{}
	service, err := NewHandoffService(repository, &sequentialIDGenerator{}, fixedClock{now: now})
	if err != nil {
		t.Fatalf("NewHandoffService: %v", err)
	}
	if _, err := service.RequestHandoff(context.Background(), " customer-1 ", " conversation-1 ", " 需要人工 "); err != nil {
		t.Fatalf("RequestHandoff: %v", err)
	}
	if repository.requestCommand.CustomerID != "customer-1" ||
		repository.requestCommand.ConversationID != "conversation-1" ||
		repository.requestCommand.Reason != "需要人工" ||
		repository.requestCommand.EventID != "cevt_1" ||
		repository.requestCommand.SystemMessageID != "msg_2" ||
		!repository.requestCommand.OccurredAt.Equal(now.UTC()) {
		t.Fatalf("unexpected handoff request command: %#v", repository.requestCommand)
	}
	if _, err := service.Takeover(context.Background(), " agent-1 ", " conversation-1 "); err != nil {
		t.Fatalf("Takeover: %v", err)
	}
	if repository.takeoverCommand.AgentID != "agent-1" || repository.takeoverCommand.EventID != "cevt_3" || repository.takeoverCommand.SystemMessageID != "msg_4" {
		t.Fatalf("unexpected takeover command: %#v", repository.takeoverCommand)
	}
	message, err := service.SendAgentMessage(context.Background(), " agent-1 ", " conversation-1 ", " 正在处理 ")
	if err != nil {
		t.Fatalf("SendAgentMessage: %v", err)
	}
	if message.Role != domain.MessageRoleAgent || repository.agentCommand.Message.Content != "正在处理" || repository.agentCommand.EventID != "cevt_6" {
		t.Fatalf("unexpected agent message command: %#v", repository.agentCommand)
	}
	if _, err := service.ResumeAI(context.Background(), " agent-1 ", " conversation-1 "); err != nil {
		t.Fatalf("ResumeAI: %v", err)
	}
	if repository.resumeCommand.EventID != "cevt_7" || repository.resumeCommand.SystemMessageID != "msg_8" {
		t.Fatalf("unexpected resume command: %#v", repository.resumeCommand)
	}
}

func TestHandoffServiceRejectsInvalidContentBeforeRepository(t *testing.T) {
	repository := &fakeHandoffRepository{}
	service, err := NewHandoffService(repository, &sequentialIDGenerator{}, fixedClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewHandoffService: %v", err)
	}
	_, err = service.RequestHandoff(context.Background(), "customer-1", "conversation-1", strings.Repeat("长", 4_001))
	assertHandoffFailure(t, err, "invalid_handoff_request")
	if repository.requestCommand.ConversationID != "" {
		t.Fatal("invalid request reached repository")
	}
	_, err = service.SendAgentMessage(context.Background(), "agent-1", "conversation-1", "  ")
	assertHandoffFailure(t, err, "invalid_agent_message")
	if repository.agentCommand.Message.ID != "" {
		t.Fatal("invalid agent message reached repository")
	}
}

func TestHandoffServiceMapsAuthorizationAndStateFailures(t *testing.T) {
	repository := &fakeHandoffRepository{err: domain.ErrNotFound}
	service, err := NewHandoffService(repository, &sequentialIDGenerator{}, fixedClock{now: time.Now()})
	if err != nil {
		t.Fatalf("NewHandoffService: %v", err)
	}
	_, err = service.Takeover(context.Background(), "agent-1", "conversation-1")
	assertHandoffFailure(t, err, "handoff_conversation_not_found")
	repository.err = domain.ErrInvalidState
	_, err = service.ResumeAI(context.Background(), "agent-1", "conversation-1")
	assertHandoffFailure(t, err, "handoff_state_conflict")
}

func assertHandoffFailure(t *testing.T, err error, code string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("unexpected failure: %v", err)
	}
}
