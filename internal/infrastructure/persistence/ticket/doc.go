// Package ticketpg 以 PostgreSQL 实现工单审批与工单记录的持久化 Port。
//
// 审批状态转换一律用带条件的 UPDATE 实现，依据受影响行数判断转换是否发生。
// 先读后写的写法无法阻止并发的重复确认：两个请求可能同时读到 pending，
// 各自认为自己是第一个。
package ticketpg
