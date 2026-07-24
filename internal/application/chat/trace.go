package chat

import (
	"context"
	"errors"
	"strings"

	domain "agent-chat/internal/domain/chat"
)

// TraceRepository 定义管理员 Run 详情所需的 Trace Port。
type TraceRepository interface {
	LoadRunTrace(context.Context, string) (domain.RunTraceSnapshot, error)
}

// TraceService 读取脱敏 Agent Run Trace。
type TraceService struct {
	repository TraceRepository
}

// NewTraceService 创建管理员 Trace 用例。
func NewTraceService(repository TraceRepository) (*TraceService, error) {
	if repository == nil {
		return nil, errors.New("run trace repository is required")
	}
	return &TraceService{repository: repository}, nil
}

// GetRunTrace 读取指定 Run 的持久化节点、模型指标和最终决策。
func (service *TraceService) GetRunTrace(
	ctx context.Context,
	runID string,
) (domain.RunTraceSnapshot, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || len(runID) > maxScopedIDLength {
		return domain.RunTraceSnapshot{}, newFailure("invalid_run_trace_request", false, nil)
	}
	trace, err := service.repository.LoadRunTrace(ctx, runID)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return domain.RunTraceSnapshot{}, err
		case errors.Is(err, domain.ErrNotFound):
			return domain.RunTraceSnapshot{}, newFailure("agent_run_not_found", false, err)
		default:
			return domain.RunTraceSnapshot{}, newFailure("load_run_trace_failed", true, err)
		}
	}
	return trace, nil
}
