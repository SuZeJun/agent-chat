package graph

import "context"

// Observer 接收 Graph 运行期产生的进度，使客户端在 Run 结束前就能观察到执行过程。
//
// 实现必须并发安全。方法不返回错误：进度事件是尽力而为的，权威结果仍由终态原子
// 提交负责，因此进度投递失败最多退化为「回答一次性出现」，不应升级为 Run 失败。
type Observer interface {
	// OnRetrieval 在检索节点完成后调用。
	OnRetrieval(ctx context.Context, evidence []Evidence)
	// OnAssessment 在 Answerability Gate 产生决策后调用。
	OnAssessment(ctx context.Context, assessment Assessment)
	// OnAnswerDelta 在回答文本产生增量时调用；非流式分支只调用一次。
	OnAnswerDelta(ctx context.Context, delta string)
}

type observerKey struct{}

// WithObserver 将 Observer 绑定到单次运行的 Context。
//
// 走 Context 而非 Input：Observer 是横切关注点，不属于用户输入数据；同时它必须
// 是每次运行独立的，无法放进 Runtime 共享的依赖里。
func WithObserver(ctx context.Context, observer Observer) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, observerKey{}, observer)
}

// ObserverFromContext 返回当前运行绑定的 Observer；未绑定时返回不做任何事的
// 实现，因此调用方无需判空。
//
// 导出该函数是为了让 Graph 之外的调用方（例如替代 Runtime 的测试替身）能够
// 复现同样的上报行为，使 Observer 的接线可被验证。
func ObserverFromContext(ctx context.Context) Observer {
	if observer, ok := ctx.Value(observerKey{}).(Observer); ok && observer != nil {
		return observer
	}
	return noopObserver{}
}

type noopObserver struct{}

func (noopObserver) OnRetrieval(context.Context, []Evidence)  {}
func (noopObserver) OnAssessment(context.Context, Assessment) {}
func (noopObserver) OnAnswerDelta(context.Context, string)    {}
