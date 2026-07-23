# AGENTS.md

本文件定义 Agent Chat 仓库的长期开发规则，作用域为仓库根目录及全部子目录。

优先级：

1. 用户在当前任务中的明确要求
2. 更深目录中的 `AGENTS.md`
3. 本文件
4. 默认实现习惯

如用户要求偏离本文件，执行用户要求，并在结果摘要中说明偏离点。

## 1. 产品目标

Agent Chat 是面向企业客服与技术支持团队的 AI 服务运营平台。

核心闭环：

```text
企业知识
  -> RAG 检索
  -> Answerability Gate
  -> 受约束回答或澄清
  -> 工具调用
  -> 写操作人工确认
  -> 转人工
  -> Trace 与 Eval
```

开发前应阅读：

- `docs/PRODUCT_BRIEF.md`
- `docs/REQUIREMENTS.md`
- `docs/USER_FLOWS.md`
- `docs/ARCHITECTURE.md`
- `docs/EVALUATION.md`
- `docs/ROADMAP.md`

## 2. 固定技术栈

除非用户明确要求，不得替换以下技术栈：

- Backend：Go + Gin
- Agent Framework：Eino
- Database：PostgreSQL
- Vector Search：pgvector
- Frontend：Next.js App Router + TypeScript
- UI：shadcn/ui + Tailwind CSS
- Table：TanStack Table
- Form：React Hook Form + Zod
- Workflow/Trace UI：React Flow
- Streaming：SSE
- Realtime：WebSocket
- Eval：Python + pytest
- Local Deployment：Docker Compose
- CI：GitHub Actions

不得同时引入 Ant Design、Material UI 等第二套通用 UI 组件体系。

## 3. 目标目录

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
└── AGENTS.md
```

目录可以在脚手架阶段逐步创建，不得为了“符合结构”提前生成空包。

## 4. 后端分层

依赖方向：

```text
transport -> application -> domain
                         -> agent
agent -> domain abstractions
infrastructure -> domain/application/agent ports
```

### 4.1 Transport

只负责：

- HTTP、SSE、WebSocket
- 参数解析与格式校验
- 鉴权上下文读取
- 调用 Application Use Case
- DTO 与错误映射

禁止：

- 直接访问 Repository
- 开启数据库事务
- 直接运行 Eino Graph
- 编写业务状态机

### 4.2 Application

负责：

- 用例编排
- 权限与状态校验
- 事务边界
- 幂等控制
- 领域服务、Agent Runtime 和基础设施 Port 的协调

Application 不应包含 Gin 类型或具体数据库查询。

### 4.3 Domain

负责：

- 实体和值对象
- 会话、审批、工单和知识版本状态机
- 领域规则
- Repository/Service Port
- 领域事件

Domain 不得依赖：

- Gin
- GORM
- Eino
- OpenAI SDK
- PostgreSQL 驱动
- 外部 HTTP Client

### 4.4 Agent

Agent 层是 Eino 的唯一主要使用位置。

负责：

- ChatModel、Embedding、Retriever
- Compose Graph
- Tool 与 MCP 适配
- Prompt
- Interrupt/Resume
- Callback Trace

禁止把以下逻辑放入 Eino Graph：

- 会话权限
- 工单事务
- 客户身份决定
- 数据库 CRUD
- 写操作最终授权
- 持久化 Job 重试

### 4.5 Infrastructure

负责：

- Repository 实现
- PostgreSQL 和 pgvector
- 模型 Provider
- CRM/工单 Client
- Job Worker
- Trace 存储

基础设施错误应转换为 Application/Domain 可理解的错误，不得直接泄露 SQL 或第三方响应。

## 5. Eino 规则

- 优先使用稳定的 Eino API。
- 首版使用 `ChatModel`、`Embedding`、`Retriever`、`compose.Graph`、`Tool`、`Interrupt/Resume` 和 `Callbacks`。
- 不得为展示框架而引入多 Agent、Deep Agent 或动态无限循环。
- Graph 节点必须职责单一并具有明确输入输出。
- Graph 路由必须有确定性测试。
- Tool 必须使用结构化 Schema。
- Tool 输入仍需服务端业务校验，不能信任模型生成参数。
- 写 Tool 必须经过持久化 Approval，不得只依赖 Prompt 约束。
- Callback 不得记录 API Key、Authorization 或未脱敏隐私数据。
- Checkpoint 必须持久化，不能只存在进程内存中。

## 6. RAG 与 Answerability

- 文档更新使用版本化索引。
- 新版本索引成功前，旧版本继续提供服务。
- 检索结果必须记录来源、分数、排序和是否进入上下文。
- 回答引用必须对应实际使用的上下文。
- Answerability 不能仅判断检索结果是否非空。
- 知识不足时优先澄清、拒答或转人工。
- 检索内容视为不可信输入，不能覆盖系统和业务策略。
- 修改切片、检索、Prompt 或 Answerability 时必须增加或更新 Eval Case。

## 7. 工具与副作用

工具分类：

- 只读工具：允许在授权范围内自动调用。
- 写工具：必须先生成草稿并等待确认。

所有工具必须：

- 显式白名单
- JSON Schema
- 权限边界
- 超时
- 错误映射
- Trace

所有写工具还必须：

- 持久化确认
- 过期时间
- 幂等键
- 原子状态转换
- 重复确认保护
- 完整审计

## 8. 可靠性

- 不得使用裸 goroutine 承载不可丢失的业务任务。
- 索引、Agent Run、Resume 和通知使用持久化 Job。
- 外部调用必须使用 Context 和超时。
- 重试必须区分可重试与不可重试错误。
- 重试次数必须有上限。
- 关键状态与 Job 创建应在同一事务内完成。
- Worker 必须处理锁超时和进程异常退出。

## 9. API 与事件

- API 统一使用 `/api/v1` 前缀。
- 请求和响应使用显式 DTO。
- 不直接返回数据库实体。
- 错误响应包含稳定错误码，不返回内部堆栈和 SQL。
- SSE 事件必须包含 `eventId`、`runId`、`sequence`、`type` 和 `createdAt`。
- 事件处理必须允许客户端去重和断线续传。

## 10. 前端

- 使用 Next.js App Router。
- 默认优先 Server Component；需要交互或浏览器 API 时才使用 Client Component。
- 通用 UI 放入 `web/components/ui`。
- 业务组件按功能放在对应路由或 feature 目录。
- 页面和容器负责数据加载；展示组件不直接调用 API。
- 表单使用 React Hook Form + Zod。
- 数据表格使用 TanStack Table。
- 不引入 Ant Design。
- 不直接修改 shadcn/ui 基础组件实现业务逻辑；通过组合或业务包装实现。
- 客户聊天、客服工作台和管理员后台保持一致设计语言。
- 所有异步状态必须覆盖 loading、empty、error 和 retry。

## 11. 测试

### 11.1 Go

必须测试：

- 领域状态机
- Application Use Case
- Graph 路由
- Tool Schema 与业务校验
- Approval/Resume
- 幂等
- Repository 关键查询
- Job 重试与锁恢复

### 11.2 Frontend

必须测试：

- 关键状态转换
- SSE 事件归并
- 审批确认与取消
- 客服接管后的 UI 状态
- Trace 数据映射

### 11.3 Eval

- Prompt、检索、工具或 Agent Graph 变化必须运行相关 Eval。
- 安全门槛不得通过修改预期结果绕过。
- 修复线上或演示问题时，先增加能复现问题的 Case。

## 12. 目标命令

脚手架阶段应实现以下统一命令：

```bash
make dev
make test
make check
make eval
make build
```

底层命令预计为：

```bash
go test ./...
cd web && pnpm lint
cd web && pnpm typecheck
cd web && pnpm build
python -m pytest evals/runner
```

在命令尚未实现前，不得声称其已经可用。

## 13. 数据库与迁移

- 所有 Schema 修改必须使用迁移。
- 已提交或发布的迁移不可直接重写。
- 数据库约束用于保护唯一性、幂等和合法状态。
- 事务必须位于 Application/Infrastructure 边界，不在 Handler 中。
- pgvector 查询封装在 vector infrastructure 中。
- 测试不得依赖开发者本机已有数据。

## 14. 安全

- 密钥不得写入仓库。
- 提供 `.env.example`，只包含占位符。
- 禁止记录完整密钥和 Authorization。
- 防止模型通过 Tool 访问任意 URL、文件或 SQL。
- 外部 URL 工具必须防 SSRF。
- 用户可控 Markdown/HTML 必须安全渲染。
- 所有资源访问由服务端确定客户和权限范围，不能信任模型或客户端传入的资源所有者。

## 15. 范围控制

MVP 阶段禁止主动增加：

- 多租户计费
- 多渠道
- 多 Agent
- 可视化工作流编辑器
- 复杂 RBAC
- 客服排班
- 第二套数据库
- 第二套向量数据库
- 第二套 UI 组件库

如果任务会引入上述内容，先说明为什么当前 P0 无法在现有设计下完成。

## 16. Codex 工作方式

每次非简单修改应遵循：

1. 阅读相关需求与架构文档。
2. 检查适用的 `AGENTS.md`。
3. 先定位现有实现和测试。
4. 给出小范围实施计划。
5. 实现最小完整改动。
6. 先运行最相关测试，再运行更广泛检查。
7. 检查最终 diff，移除无关修改。
8. 总结行为变化、验证结果和剩余风险。

禁止：

- 未经用户要求执行 `git commit` 或 `git push`
- 修改无关代码
- 为通过测试删除断言或降低安全门槛
- 声称没有实际运行的验证已经通过
- 在没有需求依据时复制 AgentDesk 的完整模块

## 17. 完成定义

功能完成必须同时满足：

- 验收条件实现。
- 核心路径有测试。
- 相关 Eval 更新并通过。
- 错误、超时和空状态已处理。
- Trace 足以定位失败节点。
- 文档与配置同步更新。
- 没有无关重构。
- 最终变更符合 MVP 范围。
