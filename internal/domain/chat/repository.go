package chat

import "context"

// Repository 定义会话创建和客户消息启动 Agent Run 的原子持久化能力。
type Repository interface {
	// CreateConversation 创建绑定客户与知识库的会话。
	CreateConversation(context.Context, Conversation) error
	// StartRun 原子创建客户消息、Agent Run、首事件和持久化 Job。
	StartRun(context.Context, StartRunSubmission) (StartRunResult, error)
	// LoadMessageHistory 在客户授权范围内读取一页会话消息和关联 Run 结果。
	LoadMessageHistory(context.Context, MessageHistoryQuery) (MessageHistoryPage, error)
	// BeginRunAttempt 锁定 Run 并记录一次 Worker 执行开始。
	BeginRunAttempt(context.Context, BeginRunAttempt) (RunSource, error)
	// CompleteRun 原子保存回答、Graph Result、事件并结束 Run。
	CompleteRun(context.Context, CompleteRunCommand) error
	// RecordRunFailure 记录可重试尝试或将 Run 终结为 failed。
	RecordRunFailure(context.Context, RecordRunFailureCommand) error
	// LoadRunEvents 在客户授权范围内按 sequence 增量读取 Run 事件。
	LoadRunEvents(context.Context, string, string, int, int) (RunEventPage, error)
	// LoadRunTrace 读取管理员可见的脱敏 Run 详情与 Eino Trace。
	LoadRunTrace(context.Context, string) (RunTraceSnapshot, error)
}
