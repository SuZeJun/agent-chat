package chat

import (
	"context"
	"errors"
	"strings"

	domain "agent-chat/internal/domain/chat"
)

const maxRunEventsPageSize = 100

// EventRepository 定义 SSE 增量读取所需的客户隔离事件 Port。
type EventRepository interface {
	LoadRunEvents(context.Context, string, string, int, int) (domain.RunEventPage, error)
}

// EventRequest 是一次 Run 事件增量读取请求。
type EventRequest struct {
	CustomerID    string
	RunID         string
	AfterSequence int
}

// EventService 在 Application 层保护客户范围和分页约束。
type EventService struct {
	repository EventRepository
}

// NewEventService 创建 Run 事件读取服务。
func NewEventService(repository EventRepository) (*EventService, error) {
	if repository == nil {
		return nil, errors.New("run event repository is required")
	}
	return &EventService{repository: repository}, nil
}

// ReadEvents 读取指定 sequence 之后的最多 100 条持久化事件。
func (service *EventService) ReadEvents(
	ctx context.Context,
	request EventRequest,
) (domain.RunEventPage, error) {
	request.CustomerID = strings.TrimSpace(request.CustomerID)
	request.RunID = strings.TrimSpace(request.RunID)
	if request.CustomerID == "" || len(request.CustomerID) > maxScopedIDLength ||
		request.RunID == "" || len(request.RunID) > maxScopedIDLength ||
		request.AfterSequence < 0 {
		return domain.RunEventPage{}, newFailure("invalid_run_event_request", false, nil)
	}
	page, err := service.repository.LoadRunEvents(
		ctx,
		request.CustomerID,
		request.RunID,
		request.AfterSequence,
		maxRunEventsPageSize,
	)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return domain.RunEventPage{}, err
		case errors.Is(err, domain.ErrNotFound):
			return domain.RunEventPage{}, newFailure("agent_run_not_found", false, err)
		default:
			return domain.RunEventPage{}, newFailure("load_run_events_failed", true, err)
		}
	}
	if page.RunID != request.RunID || !page.Status.Valid() {
		return domain.RunEventPage{}, newFailure("invalid_run_event_result", false, nil)
	}
	previous := request.AfterSequence
	for _, event := range page.Events {
		if err := event.Validate(); err != nil ||
			event.RunID != request.RunID ||
			event.Sequence <= previous {
			return domain.RunEventPage{}, newFailure("invalid_run_event_result", false, err)
		}
		previous = event.Sequence
	}
	return page, nil
}
