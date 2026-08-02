package ticket

import (
	"context"
	"time"
)

// ApproveResult 是一次确认尝试的结果。
//
// 区分「本次确认促成了执行」与「此前已确认」：前者要真正创建工单，后者必须
// 返回首次结果而不重复创建。调用方不能依据错误与否判断，因为重复确认不是错误。
type ApproveResult struct {
	Approval Approval
	// AlreadyApproved 表示本次调用之前审批就已处于 approved 状态。
	AlreadyApproved bool
}

// Repository 定义工单审批的持久化 Port。
//
// 状态转换必须在数据库层以条件更新实现，依据受影响行数判断转换是否发生；
// 先读后写的实现无法阻止并发的重复确认。
type Repository interface {
	// CreateApproval 保存待确认的工单草稿，不产生任何工单副作用。
	CreateApproval(ctx context.Context, approval Approval) error

	// LoadApproval 按客户授权范围读取审批。
	LoadApproval(ctx context.Context, customerID string, approvalID string) (Approval, error)

	// Approve 原子地把 pending 转为 approved。
	//
	// 已是 approved 时返回 AlreadyApproved 而非错误：重复确认是正常的用户行为，
	// 调用方据此返回首次结果。已取消、已过期或超出窗口时返回相应错误。
	Approve(
		ctx context.Context,
		customerID string,
		approvalID string,
		now time.Time,
	) (ApproveResult, error)

	// Cancel 原子地把 pending 转为 cancelled；已取消视为成功。
	Cancel(
		ctx context.Context,
		customerID string,
		approvalID string,
		now time.Time,
	) (Approval, error)

	// CreateTicket 在同一事务内创建工单并回填审批的工单引用。
	//
	// 幂等由审批与工单之间的一对一约束保证：同一审批第二次调用返回既有工单，
	// 而不是创建第二张。
	CreateTicket(ctx context.Context, ticket Ticket) (Ticket, error)

	// LoadTicketByApproval 读取审批已产生的工单。
	LoadTicketByApproval(ctx context.Context, approvalID string) (Ticket, error)
}
