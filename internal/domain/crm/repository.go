package crm

import "context"

// SubscriptionReader 是查询客户订阅信息的 Port。
//
// 只接受服务端注入的客户标识；实现不得提供按其他条件检索的能力，避免调用方
// 通过构造参数越权读取。
type SubscriptionReader interface {
	LoadSubscription(ctx context.Context, customerID string) (Subscription, error)
}
