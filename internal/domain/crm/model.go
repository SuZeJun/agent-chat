package crm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxCustomerIDLength = 64

var (
	// ErrNotFound 表示授权范围内不存在该客户的订阅记录。
	ErrNotFound = errors.New("subscription not found")
	// ErrUnavailable 表示 CRM 暂时不可用；调用方不得据此推测订阅状态。
	ErrUnavailable = errors.New("crm unavailable")
)

// Subscription 是客户当前的订阅快照。
//
// 字段保持扁平且自解释：它会被序列化进 Prompt 供模型组织回答，嵌套结构会增加
// 模型误读的可能。
type Subscription struct {
	CustomerID      string
	PlanName        string
	MonthlyAPIQuota int
	UsedAPICalls    int
	MemberLimit     int
	MemberCount     int
	SLAIncluded     bool
	RenewsAt        time.Time
}

// Validate 校验订阅快照的完整性，避免残缺数据进入模型上下文。
func (subscription Subscription) Validate() error {
	customerID := strings.TrimSpace(subscription.CustomerID)
	if customerID == "" || len(customerID) > maxCustomerIDLength {
		return fmt.Errorf("subscription customer ID must be 1-%d characters", maxCustomerIDLength)
	}
	if strings.TrimSpace(subscription.PlanName) == "" {
		return errors.New("subscription plan name must not be blank")
	}
	if subscription.MonthlyAPIQuota < 0 ||
		subscription.UsedAPICalls < 0 ||
		subscription.MemberLimit < 0 ||
		subscription.MemberCount < 0 {
		return errors.New("subscription counters must not be negative")
	}
	if subscription.RenewsAt.IsZero() {
		return errors.New("subscription renewal time is required")
	}
	return nil
}

// RemainingAPICalls 返回本计费周期剩余调用次数，不会低于零。
func (subscription Subscription) RemainingAPICalls() int {
	remaining := subscription.MonthlyAPIQuota - subscription.UsedAPICalls
	if remaining < 0 {
		return 0
	}
	return remaining
}
