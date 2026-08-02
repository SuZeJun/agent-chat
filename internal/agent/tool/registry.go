package agenttool

import (
	"context"
	"errors"
	"fmt"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Failure 是可安全跨 Agent 边界返回的工具失败。
//
// 只暴露工具名、稳定错误码和可重试性，不包含下游响应正文或客户数据。
type Failure struct {
	Tool         string
	Code         string
	RetryAllowed bool
	cause        error
}

// NewFailure 创建带稳定错误码的工具失败。
func NewFailure(toolName string, code string, retryAllowed bool, cause error) error {
	return &Failure{Tool: toolName, Code: code, RetryAllowed: retryAllowed, cause: cause}
}

// Error 返回不含下游细节的稳定错误描述。
func (failure *Failure) Error() string {
	return failure.Tool + ": " + failure.Code
}

// Unwrap 仅供进程内错误判断，不应记录底层 cause。
func (failure *Failure) Unwrap() error {
	return failure.cause
}

// CanRetry 返回调用方是否可以安全重试本次工具调用。
//
// 与 chat、graph 和 knowledgeretrieve 的 Failure 保持同一契约：调用方通过错误链上
// 的该方法判定重试性，缺少它会让可重试失败被静默降级为永久失败。
func (failure *Failure) CanRetry() bool {
	return failure.RetryAllowed
}

// Registry 是 Agent 可调用工具的显式白名单。
//
// 查找按精确名称进行：模型给出的未注册名称一律拒绝，不做近似匹配或回退，
// 否则白名单就失去意义。
type Registry struct {
	tools map[string]einotool.InvokableTool
	order []string
}

// NewRegistry 按给定顺序注册工具，名称重复或为空即构造失败。
func NewRegistry(tools ...einotool.InvokableTool) (*Registry, error) {
	registry := &Registry{
		tools: make(map[string]einotool.InvokableTool, len(tools)),
		order: make([]string, 0, len(tools)),
	}
	for _, item := range tools {
		if item == nil {
			return nil, errors.New("tool registry entry must not be nil")
		}
		info, err := item.Info(context.Background())
		if err != nil {
			return nil, fmt.Errorf("read tool info: %w", err)
		}
		name := strings.TrimSpace(info.Name)
		if name == "" {
			return nil, errors.New("tool name must not be blank")
		}
		if _, exists := registry.tools[name]; exists {
			return nil, fmt.Errorf("tool %q is registered twice", name)
		}
		registry.tools[name] = item
		registry.order = append(registry.order, name)
	}
	return registry, nil
}

// Infos 返回可交给模型的工具声明，顺序与注册顺序一致。
func (registry *Registry) Infos(ctx context.Context) ([]*schema.ToolInfo, error) {
	infos := make([]*schema.ToolInfo, 0, len(registry.order))
	for _, name := range registry.order {
		info, err := registry.tools[name].Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("read tool info: %w", err)
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Empty 返回注册表是否没有任何可用工具。
func (registry *Registry) Empty() bool {
	return len(registry.order) == 0
}

// Invoke 按名称执行工具；未注册的名称以稳定错误码拒绝。
func (registry *Registry) Invoke(
	ctx context.Context,
	name string,
	argumentsInJSON string,
) (string, error) {
	name = strings.TrimSpace(name)
	item, ok := registry.tools[name]
	if !ok {
		return "", NewFailure(name, "tool_not_allowed", false, errors.New("tool is not registered"))
	}
	return item.InvokableRun(ctx, argumentsInJSON)
}
