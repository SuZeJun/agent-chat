package ticketapp

import (
	"context"
	"errors"

	domain "agent-chat/internal/domain/ticket"
)

// CreationExecutor 执行 ticket.create 持久化 Job。
type CreationExecutor struct {
	repository domain.Repository
}

// NewCreationExecutor 创建幂等工单写操作执行器。
func NewCreationExecutor(repository domain.Repository) (*CreationExecutor, error) {
	if repository == nil {
		return nil, errors.New("ticket repository is required")
	}
	return &CreationExecutor{repository: repository}, nil
}

// Execute 幂等创建工单并记录 ticket.created 事件。
func (executor *CreationExecutor) Execute(
	ctx context.Context,
	command domain.ExecuteCreateCommand,
) (domain.Ticket, error) {
	created, err := executor.repository.ExecuteCreateTicket(ctx, command)
	if err != nil {
		return domain.Ticket{}, classifyApprovalError("create ticket", err)
	}
	return created, nil
}
