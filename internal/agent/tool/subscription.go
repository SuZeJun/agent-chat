package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domain "agent-chat/internal/domain/crm"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// SubscriptionToolName 是订阅查询工具的稳定名称，同时用于白名单与 Trace。
const SubscriptionToolName = "query_subscription"

const subscriptionToolDescription = "查询当前客户的订阅套餐、API 调用额度、成员数量" +
	"和续费时间。该工具不接受任何参数，客户身份由服务端根据当前会话确定。"

var _ einotool.InvokableTool = (*SubscriptionTool)(nil)

// SubscriptionTool 查询当前会话绑定客户的订阅信息。
//
// 客户标识在构造时绑定，工具本身不暴露任何入参：模型既无法指定客户，也无法
// 通过参数扩大查询范围。
type SubscriptionTool struct {
	reader     domain.SubscriptionReader
	customerID string
}

// NewSubscriptionTool 创建绑定单个客户的订阅查询工具。
func NewSubscriptionTool(
	reader domain.SubscriptionReader,
	customerID string,
) (*SubscriptionTool, error) {
	if reader == nil {
		return nil, errors.New("subscription tool reader is required")
	}
	customerID = strings.TrimSpace(customerID)
	if customerID == "" || len(customerID) > 64 {
		return nil, errors.New("subscription tool customer ID must be 1-64 characters")
	}
	return &SubscriptionTool{reader: reader, customerID: customerID}, nil
}

// Info 返回工具描述。ParamsOneOf 为 nil 表示该工具不接受任何入参。
func (tool *SubscriptionTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: SubscriptionToolName,
		Desc: subscriptionToolDescription,
	}, nil
}

// subscriptionPayload 是进入模型上下文的订阅快照。
//
// 字段扁平且带单位语义，剩余额度预先算好：让模型做算术会引入无谓的出错面。
type subscriptionPayload struct {
	PlanName            string `json:"planName"`
	MonthlyAPIQuota     int    `json:"monthlyApiQuota"`
	UsedAPICalls        int    `json:"usedApiCalls"`
	RemainingAPICalls   int    `json:"remainingApiCalls"`
	MemberLimit         int    `json:"memberLimit"`
	MemberCount         int    `json:"memberCount"`
	SLAIncluded         bool   `json:"slaIncluded"`
	RenewsAtRFC3339Date string `json:"renewsAt"`
}

// InvokableRun 执行订阅查询。
//
// 失败以错误返回而不是编造一个"查询失败"的结果串：调用方需要据此走确定性的
// 兜底回复，而不能把失败交给模型自由发挥。
func (tool *SubscriptionTool) InvokableRun(
	ctx context.Context,
	argumentsInJSON string,
	_ ...einotool.Option,
) (string, error) {
	if err := ensureNoArguments(argumentsInJSON); err != nil {
		return "", err
	}

	subscription, err := tool.reader.LoadSubscription(ctx, tool.customerID)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return "", NewFailure(SubscriptionToolName, "subscription_not_found", false, err)
		case errors.Is(err, domain.ErrUnavailable):
			return "", NewFailure(SubscriptionToolName, "crm_unavailable", true, err)
		default:
			return "", NewFailure(SubscriptionToolName, "subscription_query_failed", true, err)
		}
	}
	if err := subscription.Validate(); err != nil {
		return "", NewFailure(SubscriptionToolName, "invalid_subscription_record", false, err)
	}

	encoded, err := json.Marshal(subscriptionPayload{
		PlanName:            subscription.PlanName,
		MonthlyAPIQuota:     subscription.MonthlyAPIQuota,
		UsedAPICalls:        subscription.UsedAPICalls,
		RemainingAPICalls:   subscription.RemainingAPICalls(),
		MemberLimit:         subscription.MemberLimit,
		MemberCount:         subscription.MemberCount,
		SLAIncluded:         subscription.SLAIncluded,
		RenewsAtRFC3339Date: subscription.RenewsAt.Format("2006-01-02"),
	})
	if err != nil {
		return "", NewFailure(SubscriptionToolName, "invalid_subscription_record", false, err)
	}
	return string(encoded), nil
}

// ensureNoArguments 拒绝任何非空入参。
//
// 该工具声明为无参；模型仍可能给出内容。静默忽略会掩盖模型对工具契约的误解，
// 因此显式拒绝并给出稳定错误码。
func ensureNoArguments(argumentsInJSON string) error {
	trimmed := strings.TrimSpace(argumentsInJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(trimmed), &arguments); err != nil {
		return NewFailure(
			SubscriptionToolName,
			"invalid_tool_arguments",
			false,
			fmt.Errorf("arguments must be a JSON object"),
		)
	}
	if len(arguments) > 0 {
		return NewFailure(
			SubscriptionToolName,
			"invalid_tool_arguments",
			false,
			fmt.Errorf("tool accepts no arguments"),
		)
	}
	return nil
}
