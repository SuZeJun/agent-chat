package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	domain "agent-chat/internal/domain/crm"
)

type fakeReader struct {
	subscriptions map[string]domain.Subscription
	err           error
	requested     []string
}

func (reader *fakeReader) LoadSubscription(
	_ context.Context,
	customerID string,
) (domain.Subscription, error) {
	reader.requested = append(reader.requested, customerID)
	if reader.err != nil {
		return domain.Subscription{}, reader.err
	}
	subscription, ok := reader.subscriptions[customerID]
	if !ok {
		return domain.Subscription{}, domain.ErrNotFound
	}
	return subscription, nil
}

func testSubscription(customerID string) domain.Subscription {
	return domain.Subscription{
		CustomerID:      customerID,
		PlanName:        "免费版",
		MonthlyAPIQuota: 1000,
		UsedAPICalls:    317,
		MemberLimit:     3,
		MemberCount:     2,
		SLAIncluded:     false,
		RenewsAt:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

func newTestReader() *fakeReader {
	return &fakeReader{
		subscriptions: map[string]domain.Subscription{
			"customer-1": testSubscription("customer-1"),
			"customer-2": testSubscription("customer-2"),
		},
	}
}

// TestSubscriptionToolAlwaysQueriesBoundCustomer 锁定工具的授权边界。
//
// 客户标识在构造时绑定，模型给出的任何参数都不能改变查询目标；否则 Agent 就能
// 通过构造参数读取其他客户的订阅数据。
func TestSubscriptionToolAlwaysQueriesBoundCustomer(t *testing.T) {
	attempts := []string{
		"",
		"{}",
		`{"customerId":"customer-2"}`,
		`{"customer_id":"customer-2","plan":"专业版"}`,
	}

	for _, arguments := range attempts {
		t.Run(arguments, func(t *testing.T) {
			reader := newTestReader()
			tool, err := NewSubscriptionTool(reader, "customer-1")
			if err != nil {
				t.Fatalf("NewSubscriptionTool returned error: %v", err)
			}

			_, err = tool.InvokableRun(context.Background(), arguments)
			for _, requested := range reader.requested {
				if requested != "customer-1" {
					t.Fatalf("tool queried another customer: %q", requested)
				}
			}
			// 带参数的调用必须被拒绝，而不是忽略参数后照常返回。
			if arguments != "" && arguments != "{}" {
				assertFailureCode(t, err, "invalid_tool_arguments", false)
			} else if err != nil {
				t.Fatalf("unexpected error for empty arguments: %v", err)
			}
		})
	}
}

func TestSubscriptionToolReturnsPrecomputedRemainingQuota(t *testing.T) {
	reader := newTestReader()
	tool, err := NewSubscriptionTool(reader, "customer-1")
	if err != nil {
		t.Fatalf("NewSubscriptionTool returned error: %v", err)
	}

	result, err := tool.InvokableRun(context.Background(), "{}")
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	var payload subscriptionPayload
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("tool result is not valid JSON: %v", err)
	}
	// 剩余额度由服务端算好，避免模型做算术。
	if payload.RemainingAPICalls != 683 {
		t.Fatalf("unexpected remaining quota: %#v", payload)
	}
	if payload.RenewsAtRFC3339Date != "2026-09-01" {
		t.Fatalf("unexpected renewal date: %#v", payload)
	}
}

// TestSubscriptionToolClassifiesDownstreamFailures 保证下游失败带稳定错误码与
// 正确的可重试性，且不泄露下游细节。
func TestSubscriptionToolClassifiesDownstreamFailures(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		code      string
		retryable bool
	}{
		{"not found", domain.ErrNotFound, "subscription_not_found", false},
		{"unavailable", domain.ErrUnavailable, "crm_unavailable", true},
		{"unknown", errors.New("connection reset by peer"), "subscription_query_failed", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeReader{err: test.err}
			tool, err := NewSubscriptionTool(reader, "customer-1")
			if err != nil {
				t.Fatalf("NewSubscriptionTool returned error: %v", err)
			}

			_, err = tool.InvokableRun(context.Background(), "{}")
			assertFailureCode(t, err, test.code, test.retryable)
			if err.Error() == "connection reset by peer" {
				t.Fatal("downstream detail escaped the tool boundary")
			}
		})
	}
}

func TestSubscriptionToolDeclaresNoParameters(t *testing.T) {
	tool, err := NewSubscriptionTool(newTestReader(), "customer-1")
	if err != nil {
		t.Fatalf("NewSubscriptionTool returned error: %v", err)
	}
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info returned error: %v", err)
	}
	if info.Name != SubscriptionToolName {
		t.Fatalf("unexpected tool name: %q", info.Name)
	}
	// ParamsOneOf 为 nil 表示无入参：模型无法指定客户，也无法扩大查询范围。
	if info.ParamsOneOf != nil {
		t.Fatal("subscription tool must not declare any parameter")
	}
}

func TestNewSubscriptionToolRequiresBoundCustomer(t *testing.T) {
	if _, err := NewSubscriptionTool(newTestReader(), "   "); err == nil {
		t.Fatal("expected blank customer ID to be rejected")
	}
	if _, err := NewSubscriptionTool(nil, "customer-1"); err == nil {
		t.Fatal("expected nil reader to be rejected")
	}
}

func assertFailureCode(t *testing.T, err error, code string, retryable bool) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected tool Failure, got %v", err)
	}
	if failure.Code != code || failure.RetryAllowed != retryable {
		t.Fatalf("unexpected failure: %#v", failure)
	}
	// 调用方通过错误链上的 CanRetry 读取重试性，只断言字段无法发现方法缺失。
	var retryability interface{ CanRetry() bool }
	if !errors.As(err, &retryability) {
		t.Fatal("tool failure does not expose CanRetry to callers")
	}
	if retryability.CanRetry() != retryable {
		t.Fatalf("CanRetry disagrees with RetryAllowed: %#v", failure)
	}
}
