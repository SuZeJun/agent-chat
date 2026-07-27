package chat

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentgraph "agent-chat/internal/agent/graph"
	domain "agent-chat/internal/domain/chat"
)

// progressFlushInterval 是回答增量的合并窗口。
//
// SSE 端点按固定间隔轮询数据库，比它更细的落库频率不会提升客户端可见的粒度，
// 只会把一次回答放大成上百次写入，因此取与轮询间隔一致的值。
const progressFlushInterval = 500 * time.Millisecond

// ProgressRepository 定义运行期追加进度事件所需的能力。
type ProgressRepository interface {
	AppendRunProgress(context.Context, domain.AppendRunProgressCommand) error
}

// runProgress 把 Graph 的运行期回调转换为持久化进度事件。
//
// 进度是尽力而为的：任何投递失败只记录日志，不会中断 Run。权威结果仍由终态
// 原子提交负责，因此最坏情况只是退化为回答一次性出现。
type runProgress struct {
	repository  ProgressRepository
	idGenerator IDGenerator
	clock       Clock
	logger      *slog.Logger
	runID       string
	messageID   string

	mutex     sync.Mutex
	buffer    strings.Builder
	lastFlush time.Time
}

func newRunProgress(
	repository ProgressRepository,
	idGenerator IDGenerator,
	clock Clock,
	logger *slog.Logger,
	runID string,
	messageID string,
) *runProgress {
	return &runProgress{
		repository:  repository,
		idGenerator: idGenerator,
		clock:       clock,
		logger:      logger,
		runID:       runID,
		messageID:   messageID,
		lastFlush:   clock.Now(),
	}
}

var _ agentgraph.Observer = (*runProgress)(nil)

func (progress *runProgress) OnRetrieval(ctx context.Context, evidence []agentgraph.Evidence) {
	progress.emit(ctx, domain.EventDraft{
		ID:        progress.idGenerator.NewID("evt_"),
		Type:      domain.EventTypeRetrievalCompleted,
		Payload:   map[string]any{"evidence": evidence},
		CreatedAt: progress.clock.Now().UTC(),
	})
}

func (progress *runProgress) OnAssessment(
	ctx context.Context,
	assessment agentgraph.Assessment,
) {
	progress.emit(ctx, domain.EventDraft{
		ID:   progress.idGenerator.NewID("evt_"),
		Type: domain.EventTypeAnswerabilityDecided,
		Payload: map[string]any{
			"decision":   assessment.Decision,
			"reason":     assessment.Reason,
			"confidence": assessment.Confidence,
			"evidence":   assessment.Evidence,
		},
		CreatedAt: progress.clock.Now().UTC(),
	})
}

// OnAnswerDelta 缓冲增量，达到合并窗口后才落库。
func (progress *runProgress) OnAnswerDelta(ctx context.Context, delta string) {
	if delta == "" {
		return
	}
	progress.mutex.Lock()
	progress.buffer.WriteString(delta)
	ready := progress.clock.Now().Sub(progress.lastFlush) >= progressFlushInterval
	pending := ""
	if ready {
		pending = progress.takeBufferLocked()
	}
	progress.mutex.Unlock()

	if pending != "" {
		progress.emitDelta(ctx, pending)
	}
}

// Flush 送出尚未达到合并窗口的残余增量，必须在终态提交前调用。
func (progress *runProgress) Flush(ctx context.Context) {
	progress.mutex.Lock()
	pending := progress.takeBufferLocked()
	progress.mutex.Unlock()
	if pending != "" {
		progress.emitDelta(ctx, pending)
	}
}

func (progress *runProgress) takeBufferLocked() string {
	pending := progress.buffer.String()
	progress.buffer.Reset()
	progress.lastFlush = progress.clock.Now()
	return pending
}

func (progress *runProgress) emitDelta(ctx context.Context, delta string) {
	progress.emit(ctx, domain.EventDraft{
		ID:   progress.idGenerator.NewID("evt_"),
		Type: domain.EventTypeMessageDelta,
		Payload: map[string]any{
			"messageId": progress.messageID,
			"delta":     delta,
		},
		CreatedAt: progress.clock.Now().UTC(),
	})
}

func (progress *runProgress) emit(ctx context.Context, event domain.EventDraft) {
	// 使用不可取消的 Context：Run 被取消时仍应尽量落下已产生的进度，
	// 但不等待——超时后放弃即可。
	emitContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), progressEmitTimeout)
	defer cancel()

	err := progress.repository.AppendRunProgress(emitContext, domain.AppendRunProgressCommand{
		RunID:  progress.runID,
		Events: []domain.EventDraft{event},
	})
	if err != nil {
		progress.logger.WarnContext(ctx, "append run progress failed",
			"run_id", progress.runID,
			"event_type", event.Type,
			"error", err,
		)
	}
}

const progressEmitTimeout = 3 * time.Second
