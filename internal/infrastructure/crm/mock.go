// Package crmmock 提供演示用的内存 CRM，替代真实客户系统。
package crmmock

import (
	"context"
	"strings"
	"sync"
	"time"

	domain "agent-chat/internal/domain/crm"
)

// Reader 是并发安全的内存订阅数据源。
//
// 演示阶段不落库：订阅数据属于外部系统，用表模拟会让人误以为它是本服务的
// 权威数据。unavailable 用于验证「CRM 不可用时不得猜测订阅状态」这条约束。
type Reader struct {
	mutex         sync.RWMutex
	subscriptions map[string]domain.Subscription
	unavailable   bool
}

// NewReader 创建带演示数据的内存 CRM。
func NewReader(now time.Time) *Reader {
	renewsAt := now.AddDate(0, 1, 0).UTC()
	return &Reader{
		subscriptions: map[string]domain.Subscription{
			"demo-customer": {
				CustomerID:      "demo-customer",
				PlanName:        "免费版",
				MonthlyAPIQuota: 1000,
				UsedAPICalls:    317,
				MemberLimit:     3,
				MemberCount:     2,
				SLAIncluded:     false,
				RenewsAt:        renewsAt,
			},
			"demo-pro-customer": {
				CustomerID:      "demo-pro-customer",
				PlanName:        "专业版",
				MonthlyAPIQuota: 50000,
				UsedAPICalls:    12840,
				MemberLimit:     20,
				MemberCount:     11,
				SLAIncluded:     true,
				RenewsAt:        renewsAt,
			},
		},
	}
}

// LoadSubscription 返回指定客户的订阅快照。
//
// 仅按精确客户标识查询：不提供模糊匹配或列举能力，调用方无法借此枚举其他客户。
func (reader *Reader) LoadSubscription(
	_ context.Context,
	customerID string,
) (domain.Subscription, error) {
	reader.mutex.RLock()
	defer reader.mutex.RUnlock()

	if reader.unavailable {
		return domain.Subscription{}, domain.ErrUnavailable
	}
	subscription, ok := reader.subscriptions[strings.TrimSpace(customerID)]
	if !ok {
		return domain.Subscription{}, domain.ErrNotFound
	}
	return subscription, nil
}

// SetUnavailable 模拟 CRM 故障，供测试与本地演练使用。
func (reader *Reader) SetUnavailable(unavailable bool) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	reader.unavailable = unavailable
}
