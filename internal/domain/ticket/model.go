package ticket

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxIDLength          = 64
	maxTitleRunes        = 120
	maxDescriptionRunes  = 4000
	maxTicketNumberChars = 32
)

var (
	// ErrNotFound 表示授权范围内不存在该审批或工单。
	ErrNotFound = errors.New("ticket entity not found")
	// ErrInvalidState 表示审批当前状态不允许目标转换。
	ErrInvalidState = errors.New("invalid ticket approval state")
	// ErrExpired 表示审批已过期，不能继续执行。
	ErrExpired = errors.New("ticket approval expired")
	// ErrInvalidCommand 表示命令不满足领域契约；重试不会改变结果。
	ErrInvalidCommand = errors.New("invalid ticket command")
)

// ApprovalStatus 是人工确认的持久化状态。
type ApprovalStatus string

const (
	// ApprovalStatusPending 表示等待客户确认，尚未产生任何副作用。
	ApprovalStatusPending ApprovalStatus = "pending"
	// ApprovalStatusApproved 表示客户已确认，写操作可以执行。
	ApprovalStatusApproved ApprovalStatus = "approved"
	// ApprovalStatusCancelled 表示客户已取消，写操作永远不得执行。
	ApprovalStatusCancelled ApprovalStatus = "cancelled"
	// ApprovalStatusExpired 表示确认窗口已过，需要重新发起。
	ApprovalStatusExpired ApprovalStatus = "expired"
)

// Valid 判断审批状态是否属于已知集合。
func (status ApprovalStatus) Valid() bool {
	switch status {
	case ApprovalStatusPending,
		ApprovalStatusApproved,
		ApprovalStatusCancelled,
		ApprovalStatusExpired:
		return true
	default:
		return false
	}
}

// Terminal 判断审批是否已进入不可再转换的状态。
func (status ApprovalStatus) Terminal() bool {
	return status == ApprovalStatusCancelled || status == ApprovalStatusExpired
}

// Priority 是工单优先级。取值受限，避免模型自由发挥出无法处理的等级。
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// Valid 判断优先级是否属于已知集合。
func (priority Priority) Valid() bool {
	switch priority {
	case PriorityLow, PriorityNormal, PriorityHigh:
		return true
	default:
		return false
	}
}

// Draft 是待客户确认的工单内容。
//
// 草稿必须结构化：确认界面是安全边界的一部分，用户要能清楚看到"将要发生什么"。
// 把草稿塞进一段自由文本里，等于让模型自己描述自己要做的事。
type Draft struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    Priority `json:"priority"`
}

// Validate 校验草稿字段，确保进入确认界面的内容完整且长度受限。
func (draft Draft) Validate() error {
	title := strings.TrimSpace(draft.Title)
	if title == "" || utf8.RuneCountInString(title) > maxTitleRunes {
		return fmt.Errorf("ticket title must be 1-%d characters", maxTitleRunes)
	}
	description := strings.TrimSpace(draft.Description)
	if description == "" || utf8.RuneCountInString(description) > maxDescriptionRunes {
		return fmt.Errorf("ticket description must be 1-%d characters", maxDescriptionRunes)
	}
	if !draft.Priority.Valid() {
		return fmt.Errorf("invalid ticket priority %q", draft.Priority)
	}
	return nil
}

// Approval 是一次待确认的写操作请求。
type Approval struct {
	ID             string
	ConversationID string
	CustomerID     string
	AgentRunID     string
	Draft          Draft
	Status         ApprovalStatus
	IdempotencyKey string
	TicketID       string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	DecidedAt      *time.Time
}

// Validate 校验新建审批的完整性。
func (approval Approval) Validate() error {
	if err := validateID("approval ID", approval.ID); err != nil {
		return err
	}
	if err := validateID("conversation ID", approval.ConversationID); err != nil {
		return err
	}
	if err := validateID("customer ID", approval.CustomerID); err != nil {
		return err
	}
	if err := validateID("agent run ID", approval.AgentRunID); err != nil {
		return err
	}
	if err := approval.Draft.Validate(); err != nil {
		return err
	}
	if !approval.Status.Valid() {
		return fmt.Errorf("invalid approval status %q", approval.Status)
	}
	if err := validateID("idempotency key", approval.IdempotencyKey); err != nil {
		return err
	}
	if approval.CreatedAt.IsZero() || approval.ExpiresAt.IsZero() {
		return errors.New("approval timestamps are required")
	}
	if !approval.ExpiresAt.After(approval.CreatedAt) {
		return errors.New("approval expiry must be after creation")
	}
	return nil
}

// ExpiredAt 判断审批在给定时刻是否已过期。
//
// 过期是时间的函数而非独立状态：即便持久化状态仍是 pending，只要越过窗口就
// 不得继续执行。转换时以数据库时间为准，避免依赖调用方时钟。
func (approval Approval) ExpiredAt(now time.Time) bool {
	return !now.Before(approval.ExpiresAt)
}

// Ticket 是已创建的工单记录。
type Ticket struct {
	ID             string
	Number         string
	ConversationID string
	CustomerID     string
	ApprovalID     string
	Draft          Draft
	CreatedAt      time.Time
}

// Validate 校验工单记录的完整性。
func (ticket Ticket) Validate() error {
	if err := validateID("ticket ID", ticket.ID); err != nil {
		return err
	}
	number := strings.TrimSpace(ticket.Number)
	if number == "" || len(number) > maxTicketNumberChars {
		return fmt.Errorf("ticket number must be 1-%d characters", maxTicketNumberChars)
	}
	if err := validateID("conversation ID", ticket.ConversationID); err != nil {
		return err
	}
	if err := validateID("customer ID", ticket.CustomerID); err != nil {
		return err
	}
	if err := validateID("approval ID", ticket.ApprovalID); err != nil {
		return err
	}
	if err := ticket.Draft.Validate(); err != nil {
		return err
	}
	if ticket.CreatedAt.IsZero() {
		return errors.New("ticket created time is required")
	}
	return nil
}

// DeriveIdempotencyKey 由服务端从 Agent Run 身份派生幂等键。
//
// 两条约束：
//
//   - 不接受客户端提供的键。客户端换一个键就能绕过幂等保护，那条安全属性也就
//     不成立了。
//   - 必须由 Run ID 而非审批 ID 派生。Run 是会重试的单位，每次尝试都会生成新的
//     审批 ID；用审批 ID 派生会让同一次运行的两次尝试得到不同的键，幂等失效。
func DeriveIdempotencyKey(agentRunID string) string {
	sum := sha256.Sum256([]byte("ticket-approval:" + strings.TrimSpace(agentRunID)))
	return hex.EncodeToString(sum[:])[:maxIDLength]
}

func validateID(field string, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxIDLength {
		return fmt.Errorf("%s must be 1-%d characters", field, maxIDLength)
	}
	return nil
}
