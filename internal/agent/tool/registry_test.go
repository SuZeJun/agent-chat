package agenttool

import (
	"context"
	"errors"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type stubTool struct {
	name   string
	result string
	err    error
	calls  int
}

func (tool *stubTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: tool.name, Desc: "stub"}, nil
}

func (tool *stubTool) InvokableRun(
	_ context.Context,
	_ string,
	_ ...einotool.Option,
) (string, error) {
	tool.calls++
	return tool.result, tool.err
}

// TestRegistryRejectsToolsOutsideWhitelist 锁定白名单语义。
//
// 模型可能给出任意名称。近似匹配或回退到某个默认工具都会让白名单失去意义，
// 因此未注册的名称必须以稳定错误码直接拒绝，且不得触达任何已注册工具。
func TestRegistryRejectsToolsOutsideWhitelist(t *testing.T) {
	allowed := &stubTool{name: SubscriptionToolName, result: "{}"}
	registry, err := NewRegistry(allowed)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}

	for _, name := range []string{
		"create_ticket",
		"query_subscriptions",
		"QUERY_SUBSCRIPTION",
		"",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := registry.Invoke(context.Background(), name, "{}")
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != "tool_not_allowed" {
				t.Fatalf("expected tool_not_allowed, got %v", err)
			}
			if failure.RetryAllowed {
				t.Fatal("unregistered tool must be a permanent failure")
			}
			if allowed.calls != 0 {
				t.Fatal("rejected call reached a registered tool")
			}
		})
	}
}

func TestRegistryInvokesRegisteredTool(t *testing.T) {
	allowed := &stubTool{name: SubscriptionToolName, result: `{"planName":"免费版"}`}
	registry, err := NewRegistry(allowed)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}

	result, err := registry.Invoke(context.Background(), SubscriptionToolName, "{}")
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result != `{"planName":"免费版"}` || allowed.calls != 1 {
		t.Fatalf("unexpected invocation: result=%q calls=%d", result, allowed.calls)
	}
}

func TestRegistryExposesDeclarationsInRegistrationOrder(t *testing.T) {
	first := &stubTool{name: "alpha"}
	second := &stubTool{name: "beta"}
	registry, err := NewRegistry(first, second)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	if registry.Empty() {
		t.Fatal("registry with tools must not report empty")
	}

	infos, err := registry.Infos(context.Background())
	if err != nil {
		t.Fatalf("Infos returned error: %v", err)
	}
	if len(infos) != 2 || infos[0].Name != "alpha" || infos[1].Name != "beta" {
		t.Fatalf("unexpected declarations: %#v", infos)
	}
}

func TestNewRegistryRejectsInvalidEntries(t *testing.T) {
	if _, err := NewRegistry(&stubTool{name: "  "}); err == nil {
		t.Fatal("expected blank tool name to be rejected")
	}
	if _, err := NewRegistry(&stubTool{name: "dup"}, &stubTool{name: "dup"}); err == nil {
		t.Fatal("expected duplicate tool name to be rejected")
	}
	if _, err := NewRegistry(nil); err == nil {
		t.Fatal("expected nil tool to be rejected")
	}
}

func TestEmptyRegistryReportsEmpty(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	if !registry.Empty() {
		t.Fatal("registry without tools must report empty")
	}
}
