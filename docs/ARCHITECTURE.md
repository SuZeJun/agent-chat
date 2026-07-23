# Agent Chat 架构设计

## 1. 架构目标

系统需要同时满足：

- 展示完整 AI 应用工程能力，而不是仅封装模型 API。
- 保持 Agent 编排与业务领域解耦。
- 支持知识问答、工具调用、人工确认和转人工。
- 支持持久化任务、幂等、重试和服务重启恢复。
- 支持离线评估和运行追踪。
- 在单机 Docker Compose 环境中低成本运行。

## 2. 固定技术栈

### 2.1 后端

- Go
- Gin
- Eino
- GORM 或显式 SQL Repository
- PostgreSQL
- pgvector

### 2.2 前端

- Next.js App Router
- TypeScript
- shadcn/ui
- Tailwind CSS
- TanStack Table
- React Hook Form
- Zod
- React Flow
- Recharts

### 2.3 工程与部署

- Docker Compose
- GitHub Actions
- SSE
- WebSocket（人工工作台阶段）
- Python + pytest（离线评估）

## 3. 系统上下文

```mermaid
flowchart LR
    Customer[客户聊天页] --> Web[Next.js Web]
    Agent[客服工作台] --> Web
    Admin[管理员后台] --> Web
    Web --> API[Go API]
    API --> Runtime[Eino Agent Runtime]
    API --> Domain[业务服务]
    Runtime --> LLM[OpenAI-compatible Model]
    Runtime --> Vector[pgvector]
    Runtime --> CRM[模拟 CRM / MCP]
    Domain --> DB[(PostgreSQL)]
    Eval[Python Eval Runner] --> API
    Eval --> DB
```

## 4. 分层

```text
Transport
  -> Application
    -> Domain
    -> Agent Runtime
      -> Infrastructure
```

### 4.1 Transport

职责：

- HTTP、SSE 和 WebSocket 协议处理
- 鉴权上下文解析
- 请求参数校验
- DTO 转换
- 错误响应映射

禁止：

- 直接访问数据库
- 编排业务事务
- 直接调用 Eino Graph

### 4.2 Application

职责：

- 用例编排
- 事务边界
- 权限与状态校验
- 调用领域服务和 Agent Runtime
- 创建持久化任务
- 幂等控制

典型用例：

- `SendCustomerMessage`
- `ResumeApproval`
- `TakeOverConversation`
- `CreateKnowledgeDocument`
- `RunEvaluationSuite`

### 4.3 Domain

职责：

- 会话状态机
- 消息和工单业务规则
- 审批状态机
- 知识文档版本规则
- 幂等键与领域事件

约束：

- 不依赖 Gin、GORM、Eino、OpenAI SDK 或具体数据库。
- 领域规则应通过单元测试直接验证。

### 4.4 Agent Runtime

职责：

- Eino ChatModel
- Eino Retriever 和 Embedding 适配
- Eino Compose Graph
- Tool 与 MCP 适配
- Interrupt、Checkpoint 和 Resume
- Callback 与运行 Trace
- Prompt 和模型配置解析

Agent Runtime 不拥有：

- 会话、工单和用户的业务事务
- HTTP 请求处理
- 业务权限判断
- 外部副作用的最终授权

### 4.5 Infrastructure

职责：

- PostgreSQL Repository
- pgvector Retriever
- 模型 Provider
- CRM Client
- 工单 Client
- 持久化 Job Worker
- 日志和 Trace 存储

## 5. Eino 使用边界

### 5.1 采用的 Eino 能力

- `ChatModel`：屏蔽模型供应商差异。
- `Embedding`：生成查询与文档向量。
- `Retriever`：将 pgvector 检索暴露为统一组件。
- `compose.Graph`：编排可控的客服工作流。
- `Tool`：定义只读查询和写操作草稿。
- `Interrupt/Resume`：实现用户确认。
- `Callbacks`：收集节点、模型、工具和错误 Trace。

首版模型 Provider 固定为：

- `deepseek-v4-flash` 非思考模式负责 RAG 回答，复杂模型路由需在 Eval 证明收益后再引入。
- 智谱 `embedding-3` 生成 1024 维向量，索引与查询必须使用相同模型和维度。
- 每个索引版本必须持久化 `provider + model + dimensions`，查询前必须与当前 Embedder 身份完整匹配。
- Provider 配置位于通用 Config，Eino 和供应商协议实现仅位于 Infrastructure/Agent 边界。
- API Key 仅通过环境变量注入，不进入日志、Trace、错误正文或持久化配置。

知识版本发布使用逻辑文档的 `active_version_id`：

- 新版本创建和 `knowledge.index` Job 写入处于同一事务。
- 切片替换和版本变为 `ready` 处于同一事务。
- 只有属于当前文档且状态为 `ready` 的版本可以成为活动版本。
- 新版本发布前，旧活动版本继续参与检索；发布后检索只连接新的活动版本。
- 检索事务先校验所有活动版本的 Embedding 身份，再执行 pgvector 查询，避免同维度不同模型静默混用。

### 5.2 首版不采用

- 多 Agent 自主协作
- Deep Agent
- 自动规划和无限循环
- 动态生成任意工具
- 可视化工作流编辑
- 处于实验阶段且会增加迁移成本的 API

### 5.3 Agent Graph

```mermaid
flowchart TD
    Start([Start]) --> Understand[Understand Message]
    Understand --> Route{Intent}
    Route -->|Knowledge Question| Retrieve[Retrieve Knowledge]
    Retrieve --> Gate[Answerability Gate]
    Gate -->|Answerable| Generate[Grounded Generate]
    Gate -->|Clarify| Clarify[Ask Clarifying Question]
    Gate -->|Unanswerable| Fallback[Refuse / Suggest Human]
    Route -->|Subscription Query| QueryTool[Query Subscription Tool]
    QueryTool --> GenerateToolReply[Generate Tool Reply]
    Route -->|Create Ticket| Draft[Prepare Ticket Draft]
    Draft --> Confirm[Interrupt for Confirmation]
    Confirm -->|Confirmed| CreateTicket[Create Ticket Tool]
    Confirm -->|Cancelled| CancelReply[Cancel Reply]
    Route -->|Human Requested| Handoff[Prepare Handoff]
    Generate --> End([End])
    Clarify --> End
    Fallback --> End
    GenerateToolReply --> End
    CreateTicket --> End
    CancelReply --> End
    Handoff --> End
```

## 6. 可靠执行

### 6.1 在线请求

在线请求只负责：

1. 保存客户消息。
2. 创建 Agent Run 和持久化 Job。
3. 返回 SSE 连接或运行 ID。

Worker 负责真正执行 Agent Graph，避免裸 goroutine 在服务重启时丢失任务。

### 6.2 Job 状态

```text
pending -> running -> succeeded
                   -> retry_wait -> running
                   -> failed
```

每个 Job 需要：

- 唯一任务 ID
- 任务类型
- 业务幂等键
- Payload
- 尝试次数
- 下次执行时间
- 锁定时间与 Worker
- 最后错误

执行与恢复约束：

- Worker 只领取和恢复当前进程已注册的任务类型，未支持的类型保持原状态。
- 领取使用 `FOR UPDATE SKIP LOCKED`，状态、尝试次数和租约在同一 SQL 中原子更新。
- 只有租约持有者可以提交成功或失败，防止锁恢复后的旧 Worker 覆盖新结果。
- 可重试错误使用有上限的指数退避；不可重试错误或耗尽次数后进入 `failed`。
- Handler 原始错误不得进入日志或数据库，只保存稳定错误码。
- 单次执行超时必须小于锁超时；进程异常退出后，其他 Worker 将过期任务恢复为 `retry_wait` 或 `failed`。
- Handler 必须保证副作用幂等，因为业务操作成功后、任务状态提交前崩溃会造成再次投递。

### 6.3 幂等

- 客户消息使用客户端消息 ID 去重。
- Agent Run 对来源消息建立唯一约束。
- 创建工单使用确认请求 ID 作为幂等键。
- Resume 使用审批版本或原子状态更新防止重复消费。

### 6.4 事务

以下操作必须在事务中完成：

- 消息入库、会话状态更新和 Agent Job 创建。
- 审批确认和 Resume Job 创建。
- 转人工事件、会话状态更新和通知任务创建。
- 文档版本创建和索引任务创建。

## 7. 数据模型

### 7.1 会话

- `customers`
- `conversations`
- `messages`
- `conversation_events`
- `handoff_summaries`

### 7.2 知识

- `knowledge_bases`
- `knowledge_documents`
- `knowledge_document_versions`
- `knowledge_chunks`
- `knowledge_index_jobs`

`knowledge_chunks` 包含 pgvector 向量列、文档版本、元数据和活动状态。

### 7.3 Agent

- `agent_configs`
- `agent_config_versions`
- `agent_runs`
- `agent_run_steps`
- `retrieval_traces`
- `tool_calls`
- `approval_requests`

### 7.4 工单与工具

- `tickets`
- `ticket_events`
- `customer_subscriptions`
- `jobs`

### 7.5 Eval

- `eval_suites`
- `eval_cases`
- `eval_runs`
- `eval_results`

首版 Eval Case 也可以使用版本控制中的 JSONL，数据库只保存运行结果。

## 8. API 设计

### 8.1 客户端

```text
POST /api/v1/conversations
POST /api/v1/conversations/{id}/messages
GET  /api/v1/conversations/{id}/events
POST /api/v1/approvals/{id}/confirm
POST /api/v1/approvals/{id}/cancel
POST /api/v1/conversations/{id}/request-human
```

### 8.2 客服端

```text
GET  /api/v1/agent/conversations
POST /api/v1/agent/conversations/{id}/takeover
POST /api/v1/agent/conversations/{id}/messages
POST /api/v1/agent/conversations/{id}/resume-ai
```

### 8.3 管理端

```text
POST /api/v1/admin/knowledge/documents
GET  /api/v1/admin/knowledge/documents
GET  /api/v1/admin/agent-runs/{id}
POST /api/v1/admin/evals/runs
GET  /api/v1/admin/evals/runs/{id}
```

## 9. SSE 协议

建议事件：

```text
run.started
run.status
message.delta
message.citation
tool.started
tool.completed
approval.required
handoff.created
run.completed
run.failed
```

每个事件包含：

```json
{
  "eventId": "evt_xxx",
  "runId": "run_xxx",
  "sequence": 1,
  "type": "message.delta",
  "createdAt": "2026-01-01T00:00:00Z",
  "payload": {}
}
```

`sequence` 用于断线重连和客户端去重。

## 10. Trace

统一关联字段：

- `request_id`
- `conversation_id`
- `message_id`
- `run_id`
- `step_id`
- `tool_call_id`
- `job_id`

Trace 数据分为：

- 业务事件
- Eino Callback 事件
- 检索明细
- 工具调用
- 审批与恢复
- 错误与重试

敏感数据写入前必须脱敏。

## 11. 安全设计

- 系统 Prompt 与检索内容分离，检索内容标记为不可信参考资料。
- Tool 参数必须通过结构化 Schema 和服务端校验。
- Agent 不能决定当前客户身份和权限范围。
- 工具使用白名单，写操作额外要求确认。
- 模型输出不能直接拼接 SQL、URL 或系统命令执行。
- 外部 HTTP 工具限制目标地址并防止 SSRF。
- Trace 不记录 API Key、Authorization 和完整敏感字段。

## 12. 推荐目录

```text
.
├── cmd/
│   ├── api/
│   └── worker/
├── internal/
│   ├── bootstrap/
│   ├── transport/http/
│   ├── application/
│   ├── domain/
│   ├── agent/
│   │   ├── graph/
│   │   ├── components/
│   │   ├── tools/
│   │   ├── prompts/
│   │   └── tracing/
│   ├── infrastructure/
│   │   ├── persistence/
│   │   ├── model/
│   │   ├── vector/
│   │   └── crm/
│   └── pkg/
├── migrations/
├── web/
├── evals/
│   ├── cases/
│   ├── runner/
│   └── reports/
├── docs/
├── docker-compose.yml
├── Makefile
└── AGENTS.md
```

## 13. 架构决策记录

重要决策应写入 `docs/adr/`：

- 为什么选择 Eino。
- 为什么首版选择 pgvector。
- 为什么写操作必须中断确认。
- 为什么使用持久化 Job，而不是裸 goroutine。
- 为什么首版不做多 Agent 和可视化工作流。
