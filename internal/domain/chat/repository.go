package chat

import "context"

// Repository 定义会话创建和客户消息启动 Agent Run 的原子持久化能力。
type Repository interface {
	// CreateConversation 创建绑定客户与知识库的会话。
	CreateConversation(context.Context, Conversation) error
	// StartRun 原子创建客户消息、Agent Run、首事件和持久化 Job。
	StartRun(context.Context, StartRunSubmission) (StartRunResult, error)
}
