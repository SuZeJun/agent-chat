# 代码库导航

本文档说明当前工程中各目录和文件的职责，帮助开发者快速找到入口、依赖组装、协议层和数据库实现。

## 建议阅读顺序

API 启动链路：

```text
cmd/api/main.go
  -> internal/bootstrap/api.go
  -> internal/bootstrap/runtime.go
  -> internal/infrastructure/persistence/
  -> internal/transport/http/
```

Worker 启动链路：

```text
cmd/worker/main.go
  -> internal/bootstrap/worker.go
  -> internal/bootstrap/runtime.go
  -> internal/infrastructure/jobs/worker.go
  -> internal/infrastructure/jobs/knowledge_handler.go
  -> internal/application/knowledgeindex/
  -> internal/infrastructure/model/
  -> internal/infrastructure/persistence/knowledge/
```

FAQ 导入与索引链路：

```text
internal/transport/http/knowledge_handler.go
  -> internal/application/knowledgeimport/service.go
  -> internal/infrastructure/persistence/knowledge/import.go
  -> jobs 表中的 knowledge.index
  -> internal/infrastructure/jobs/knowledge_handler.go
  -> internal/application/knowledgeindex/indexer.go
  -> internal/infrastructure/model/zhipu.go
  -> internal/infrastructure/persistence/knowledge/repository.go
```

客户问答与 SSE 链路：

```text
internal/transport/http/chat_handler.go
  -> internal/application/chat/service.go
  -> internal/infrastructure/persistence/chat/repository.go
  -> jobs 表中的 agent.run
  -> internal/infrastructure/jobs/agent_run_handler.go
  -> internal/application/chat/executor.go
  -> internal/agent/graph/
  -> internal/infrastructure/persistence/chat/execution.go
  -> internal/transport/http/chat_handler.go 的 SSE
```

工单审批与执行链路：

```text
internal/agent/tool/ticket.go
  -> internal/application/chat/executor.go 原子保存 Run 与审批
  -> internal/transport/http/ticket_handler.go 确认或取消
  -> internal/application/ticket/service.go 原子创建 ticket.create Job
  -> internal/infrastructure/jobs/ticket_handler.go
  -> internal/application/ticket/executor.go
  -> internal/infrastructure/persistence/ticket/repository.go 幂等创建工单
```

阅读具体函数时，优先关注注释中的事务、幂等、权限、Answerability、引用和 Trace 约束；参数转换、简单字段映射等私有辅助函数不会为了注释而重复描述代码。

## 根目录

| 文件 | 职责 |
| --- | --- |
| `README.md` | 项目介绍、本地启动、验证方式和当前里程碑 |
| `AGENTS.md` | 架构边界、编码规则、测试要求和 Git 工作流 |
| `.env.example` | 可提交的本地环境变量示例 |
| `.gitignore` | 排除密钥、缓存、构建产物和 IDE 配置 |
| `.gitattributes` | 固定迁移 SQL 的跨平台换行规则 |
| `go.mod` | Go 模块名称、Go 版本和直接依赖 |
| `go.sum` | Go 依赖完整性校验 |
| `Makefile` | POSIX 环境下的开发、测试和构建入口 |
| `docker-compose.yml` | 本地 pgvector PostgreSQL 服务 |

## 进程入口

| 文件 | 职责 |
| --- | --- |
| `cmd/api/doc.go` | API 命令包说明 |
| `cmd/api/main.go` | 接收退出信号并调用 `bootstrap.RunAPI` |
| `cmd/worker/doc.go` | Worker 命令包说明 |
| `cmd/worker/main.go` | 接收退出信号并调用 `bootstrap.RunWorker` |

## Bootstrap

| 文件 | 职责 |
| --- | --- |
| `internal/bootstrap/doc.go` | Bootstrap 包职责说明 |
| `internal/bootstrap/runtime.go` | 加载配置，创建 Logger、数据库连接池并执行迁移 |
| `internal/bootstrap/api.go` | 组装 Router、启动 HTTP Server 并处理优雅退出 |
| `internal/bootstrap/worker.go` | 组装并运行后台 Worker |
| `internal/bootstrap/api_integration_test.go` | 验证 API 使用真实数据库启动和退出 |

## HTTP Transport

| 文件 | 职责 |
| --- | --- |
| `internal/transport/http/doc.go` | HTTP Transport 包职责说明 |
| `internal/transport/http/router.go` | 创建 Gin Router，注册存活和就绪检查 |
| `internal/transport/http/middleware.go` | 生成 request ID、记录访问日志并恢复 panic |
| `internal/transport/http/router_test.go` | 验证健康检查、request ID 和 panic 日志安全 |
| `internal/transport/http/knowledge_handler.go` | 列出知识库、导入 FAQ CSV 并查询逐行索引状态 |
| `internal/transport/http/knowledge_handler_test.go` | 验证管理员身份、知识库 DTO、上传和可读校验错误 |
| `internal/transport/http/markdown_handler.go` | 提供知识库作用域内的 Markdown 列表、创建、版本和失败重试 API |
| `internal/transport/http/markdown_handler_test.go` | 验证 Markdown DTO、管理员身份和请求体大小上限 |
| `internal/transport/http/chat_handler.go` | 创建会话、发送消息、按客户分页恢复历史及输出 Run SSE |
| `internal/transport/http/chat_handler_test.go` | 验证历史作用域、分页参数、Run Result DTO 与聊天接口 |
| `internal/transport/http/ticket_handler.go` | 查询、确认和取消客户所属的工单审批 |
| `internal/transport/http/ticket_handler_test.go` | 验证异步确认、完成查询和过期错误映射 |

## PostgreSQL Persistence

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/persistence/doc.go` | Persistence 包职责说明 |
| `internal/infrastructure/persistence/postgres.go` | 创建并验证 pgx PostgreSQL 连接池 |
| `internal/infrastructure/persistence/migrator.go` | 加载、校验并串行执行嵌入式 SQL 迁移 |
| `internal/infrastructure/persistence/migrator_test.go` | 验证迁移文件解析、排序和空文件拒绝 |
| `internal/infrastructure/persistence/migrator_integration_test.go` | 使用真实 PostgreSQL 验证并发、幂等、回滚和约束 |

## Knowledge Domain

| 文件 | 职责 |
| --- | --- |
| `internal/domain/knowledge/doc.go` | Knowledge Domain 包职责说明 |
| `internal/domain/knowledge/model.go` | 定义知识库、文档、不可变版本、切片、Embedding 身份和检索结果 |
| `internal/domain/knowledge/repository.go` | 定义版本写入、索引完成、原子发布和活动切片检索 Port |
| `internal/domain/knowledge/model_test.go` | 验证内容校验和、向量有限值和 Embedding 身份比较 |

## Chat Domain

| 文件 | 职责 |
| --- | --- |
| `internal/domain/chat/doc.go` | Chat Domain 包职责说明 |
| `internal/domain/chat/model.go` | 定义会话、消息、Agent Run、运行事件、状态和原子提交契约 |
| `internal/domain/chat/history.go` | 定义客户作用域历史查询、消息与 Run 恢复快照及分页契约 |
| `internal/domain/chat/repository.go` | 定义会话创建和消息启动 Run 的持久化 Port |
| `internal/domain/chat/model_test.go` | 验证聊天状态、关联 ID 和初始事件约束 |

## Ticket Domain

| 文件 | 职责 |
| --- | --- |
| `internal/domain/ticket/model.go` | 定义草稿、审批、工单、状态机和持久化执行命令 |
| `internal/domain/ticket/repository.go` | 定义审批确认、取消、查询和幂等建单 Port |
| `internal/domain/ticket/model_test.go` | 验证审批终态和 Job 命令约束 |

## Knowledge Application

| 文件 | 职责 |
| --- | --- |
| `internal/application/knowledgeindex/doc.go` | Knowledge Index Application 包说明 |
| `internal/application/knowledgeindex/chunker.go` | 实现 FAQ/Markdown 确定性切片和检索元数据生成 |
| `internal/application/knowledgeindex/indexer.go` | 编排批量 embedding、切片替换、失败分类和单调发布 |
| `internal/application/knowledgeindex/chunker_test.go` | 读取版本化 Eval Case 验证切片稳定性 |
| `internal/application/knowledgeindex/indexer_test.go` | 验证幂等、批处理和错误分类 |
| `internal/application/knowledgeindex/indexer_integration_test.go` | 使用真实 PostgreSQL 验证索引、检索和乱序发布 |
| `internal/application/knowledgeindex/testdata/chunking_cases.json` | 固定 FAQ/Markdown 切片 Eval Case |
| `internal/application/knowledgeretrieve/doc.go` | Knowledge Retrieval Application 包说明 |
| `internal/application/knowledgeretrieve/service.go` | 编排问题 embedding、向量空间校验和活动切片检索 |
| `internal/application/knowledgeretrieve/service_test.go` | 验证请求、空结果、过滤、错误分类和检索 Eval Case |
| `internal/application/knowledgeretrieve/testdata/retrieval_cases.json` | 固定来源期望和元数据过滤检索 Eval Case |
| `internal/application/knowledgebase/service.go` | 创建并列出管理员可见的 Knowledge Base |
| `internal/application/knowledgeimport/service.go` | 解析、规范化并按内容幂等导入 FAQ CSV |
| `internal/application/knowledgeimport/service_test.go` | 验证 CSV 表头、字段、URL、大小和规范化校验和 |
| `internal/application/knowledgedocument/service.go` | 校验并编排 Markdown 逻辑文档、不可变版本和重试用例 |
| `internal/application/knowledgedocument/service_test.go` | 验证 Markdown 规范化、作用域、冲突和错误映射 |

## Chat Application

| 文件 | 职责 |
| --- | --- |
| `internal/application/chat/doc.go` | Chat Application 包职责说明 |
| `internal/application/chat/service.go` | 规范化客户消息并编排 Message、Run、Event 和 Job 原子创建 |
| `internal/application/chat/service_test.go` | 验证提交构造、幂等结果、请求校验和稳定错误映射 |
| `internal/application/chat/executor.go` | 编排持久化 Agent Run 尝试、RAG Graph、失败分类和终态提交 |
| `internal/application/chat/executor_test.go` | 验证成功事件、终态重放、重试耗尽和人工接管保护 |
| `internal/application/chat/history.go` | 校验客户作用域并读取可恢复 Answerability 与引用的消息历史 |
| `internal/application/chat/conversation.go` | 创建绑定当前客户和知识库的 AI 会话 |
| `internal/application/chat/events.go` | 在客户范围内增量读取 Run Event |
| `internal/application/chat/trace.go` | 读取管理员可见的脱敏 Run Trace |

## Ticket Application

| 文件 | 职责 |
| --- | --- |
| `internal/application/ticket/service.go` | 编排客户归属校验、确认与取消，并原子创建写操作 Job |
| `internal/application/ticket/executor.go` | 将 `ticket.create` Job 适配为可重试的幂等建单用例 |
| `internal/application/ticket/service_test.go` | 验证异步确认、重复确认、过期和完成查询 |

## Agent Runtime

| 文件 | 职责 |
| --- | --- |
| `internal/agent/retrieval/doc.go` | Eino Knowledge Retriever 包说明 |
| `internal/agent/retrieval/retriever.go` | 将 Application 检索结果适配为带来源和分数的 Eino Document |
| `internal/agent/retrieval/retriever_test.go` | 验证 Eino Options、资源绑定、防覆盖和 Document 映射 |
| `internal/agent/graph/doc.go` | Eino RAG Graph 包说明 |
| `internal/agent/graph/graph.go` | 编排检索、Answerability Gate、受约束生成、追问和拒答路由 |
| `internal/agent/graph/factory.go` | 按会话知识库创建资源隔离的 RAG Runtime |
| `internal/agent/graph/tracing.go` | 使用 Eino Callback 采集节点、模型耗时和 Token |
| `internal/agent/graph/answerability.go` | 按明确阈值生成三类 Answerability 决策 |
| `internal/agent/graph/evidence.go` | 校验检索排序与来源元数据，并限制进入 Prompt 的上下文 |
| `internal/agent/graph/prompt.go` | 构造不可信知识数据边界并校验回答来源标记 |
| `internal/agent/graph/types.go` | 定义 Graph 输入、输出、证据、引用、配置和稳定错误 |
| `internal/agent/graph/answerability_test.go` | 读取版本化 Eval Case 验证 Answerability 边界 |
| `internal/agent/graph/factory_test.go` | 验证生产工具注册表包含订阅查询和工单草稿工具 |
| `internal/agent/graph/graph_test.go` | 验证 Graph 路由、引用映射、Prompt Injection 边界和错误脱敏 |
| `internal/agent/graph/testdata/answerability_cases.json` | 固定三类 Answerability 路由 Eval Case |
| `internal/agent/graph/testdata/ticket_approval_cases.json` | 固定工单草稿待确认路由 Eval Case |

## Knowledge Persistence

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/persistence/knowledge/doc.go` | Knowledge PostgreSQL Repository 包说明 |
| `internal/infrastructure/persistence/knowledge/repository.go` | 实现知识库列表、版本与 Job 原子创建、切片替换、发布和 pgvector 检索 |
| `internal/infrastructure/persistence/knowledge/repository_integration_test.go` | 使用真实 PostgreSQL 验证知识库列表、版本生命周期、原子回滚和活动版本检索 |
| `internal/infrastructure/persistence/knowledge/import.go` | 原子创建 FAQ Import、文档、版本和索引 Job，并聚合状态 |
| `internal/infrastructure/persistence/knowledge/import_integration_test.go` | 验证重复导入、并发幂等和失败状态 |
| `internal/infrastructure/persistence/knowledge/markdown.go` | 以事务创建 Markdown 文档/版本与索引 Job，聚合版本状态并重置失败 Job |
| `internal/infrastructure/persistence/knowledge/markdown_integration_test.go` | 使用真实 PostgreSQL 验证事务、不可变版本、旧版本服务和知识库作用域 |

## Chat Persistence

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/persistence/chat/doc.go` | Chat PostgreSQL Repository 包说明 |
| `internal/infrastructure/persistence/chat/repository.go` | 使用会话行锁原子创建 Message、Run、首事件和持久化 Job |
| `internal/infrastructure/persistence/chat/repository_integration_test.go` | 使用真实 PostgreSQL 验证幂等、客户隔离、并发去重和整笔回滚 |
| `internal/infrastructure/persistence/chat/execution.go` | 原子管理 Run 尝试、Assistant Message、Graph Result、事件和失败终态 |
| `internal/infrastructure/persistence/chat/execution_integration_test.go` | 使用真实 PostgreSQL 验证完成、重试、终止、重放和人工接管 |
| `internal/infrastructure/persistence/chat/history.go` | 分页读取消息与 Run Result，并为规划节点截取源消息之前的历史 |
| `internal/infrastructure/persistence/chat/history_integration_test.go` | 真库验证客户隔离、游标分页、结果恢复与重试历史边界 |
| `internal/infrastructure/persistence/chat/events.go` | 按客户范围和 sequence 查询 SSE 事件 |
| `internal/infrastructure/persistence/chat/trace.go` | 查询 Run 关联 ID、Graph Result 和节点 Trace |

## Ticket Persistence

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/persistence/ticket/repository.go` | 原子确认并入队，以数据库时间判过期，并由 Worker 幂等建单 |
| `internal/infrastructure/persistence/ticket/repository_integration_test.go` | 真库验证四条审批安全属性、时钟反向路径、并发确认和 Job 重投 |

## Worker

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/jobs/doc.go` | Jobs 包职责说明 |
| `internal/infrastructure/jobs/job.go` | 定义 Job、Handler、稳定错误分类和幂等执行契约 |
| `internal/infrastructure/jobs/queue.go` | 使用 PostgreSQL 租约原子领取、完成、重试和恢复任务 |
| `internal/infrastructure/jobs/worker.go` | 管理类型分发、执行超时、有界退避和 Context 生命周期 |
| `internal/infrastructure/jobs/knowledge_handler.go` | 校验 `knowledge.index` Payload 并适配 Application 索引用例 |
| `internal/infrastructure/jobs/agent_run_handler.go` | 校验 `agent.run` Payload、幂等键和尝试信息并调用执行用例 |
| `internal/infrastructure/jobs/ticket_handler.go` | 校验 `ticket.create` 稳定 Payload 并调用幂等建单用例 |
| `internal/infrastructure/jobs/worker_test.go` | 验证成功、重试、永久失败、取消收尾和类型过滤 |
| `internal/infrastructure/jobs/knowledge_handler_test.go` | 验证索引 Payload、幂等键和失败分类映射 |
| `internal/infrastructure/jobs/agent_run_handler_test.go` | 验证 Run 任务分发、输入拒绝和失败分类映射 |
| `internal/infrastructure/jobs/ticket_handler_test.go` | 验证建单任务分发、幂等键和失败分类映射 |
| `internal/infrastructure/jobs/queue_integration_test.go` | 使用真实 PostgreSQL 验证并发领取、租约、重试上限和锁恢复 |

Worker 只领取 Bootstrap 已注册的 Job 类型。开发环境缺少 `EMBEDDING_API_KEY` 时不注册 `knowledge.index` Handler；缺少 `LLM_API_KEY` 或 `EMBEDDING_API_KEY` 时不注册 `agent.run` Handler，已有对应任务保持 `pending`。

## 通用包

| 文件 | 职责 |
| --- | --- |
| `internal/pkg/config/doc.go` | Config 包职责说明 |
| `internal/pkg/config/config.go` | 加载、解析并校验环境变量 |
| `internal/pkg/config/config_test.go` | 验证默认配置、覆盖配置和生产安全约束 |
| `internal/pkg/logx/doc.go` | Logx 包职责说明 |
| `internal/pkg/logx/logger.go` | 创建统一 JSON 结构化 Logger |

## 数据库迁移

| 文件 | 职责 |
| --- | --- |
| `migrations/doc.go` | Migrations 包职责说明 |
| `migrations/embed.go` | 使用 `go:embed` 将 SQL 迁移打入二进制 |
| `migrations/000001_init.sql` | 创建 pgvector 扩展、Job 表、约束和索引 |
| `migrations/000002_knowledge.sql` | 创建知识库、文档版本、1024 维切片、活动版本约束和 HNSW 索引 |
| `migrations/000003_chat.sql` | 创建会话、消息、Agent Run、运行事件、状态约束和幂等索引 |
| `migrations/000004_agent_run_execution.sql` | 关联 Assistant Message 与 Run，并保证每个 Run 只有一个回答 |
| `migrations/000005_faq_imports.sql` | 创建内容幂等 FAQ Import 和逐行实体关联 |
| `migrations/000006_agent_run_trace.sql` | 增加 Request ID 和节点/模型 Trace 表 |
| `migrations/000007_ticket_approvals.sql` | 创建工单审批、工单记录和写操作安全约束 |
| `migrations/000008_ticket_approval_events.sql` | 扩展审批与工单 Run Event 类型约束 |

已经提交或执行的迁移文件不可直接改写；后续 Schema 变化必须新增版本文件。

## 模型 Provider

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/model/doc.go` | Model Provider 包职责说明 |
| `internal/infrastructure/model/deepseek.go` | 将 DeepSeek OpenAI 兼容接口适配为 Eino ToolCallingChatModel |
| `internal/infrastructure/model/deepseek_test.go` | 验证模型、鉴权和 thinking 参数请求 |
| `internal/infrastructure/model/zhipu.go` | 将智谱 Embeddings API 适配为固定模型和维度的 Eino Embedder |
| `internal/infrastructure/model/zhipu_test.go` | 验证批量向量顺序、模型覆盖保护和错误脱敏 |

## Web 前端

| 文件 | 职责 |
| --- | --- |
| `web/components/chat-panel.tsx` | 恢复客户会话、发送消息并按 Run 独立订阅 SSE |
| `web/components/faq-admin-panel.tsx` | 选择知识库、校验并上传 CSV，以 TanStack Table 展示逐行索引状态 |
| `web/components/markdown-document-panel.tsx` | 创建 Markdown 文档与新版本，展示索引/活动状态并重试失败版本 |
| `web/components/assistant-message.tsx` | 展示回答、引用、三类 Answerability 与工单审批入口 |
| `web/components/ticket-approval-card.tsx` | 结构化展示草稿，按服务端状态确认、取消并轮询工单结果 |
| `web/components/run-trace-view.tsx` | 展示节点、检索、Token、工具调用和审批事件时间线 |
| `web/lib/run-events.ts` | 将有序 SSE 事件归约为可恢复的聊天状态 |
| `web/lib/message-history.ts` | 将持久化消息与 Graph Result 恢复为聊天界面状态 |
| `web/lib/ticket-approval-server.ts` | 注入客户身份并转发审批查询、确认与取消请求 |
| `web/lib/knowledge-admin-server.ts` | 注入管理员身份并限时转发知识库、FAQ 与 Markdown 管理请求 |
| `web/lib/bounded-request.ts` | 在 BFF 解析前以流式读取限制上传请求体大小 |
| `web/app/api/ticket-approvals/[approvalId]/` | 客户审批 BFF 路由，不接受客户端覆盖客户身份或草稿 |
| `web/app/admin/knowledge/page.tsx` | FAQ/Markdown 知识管理与索引状态页面 |

## 开发脚本与 CI

| 文件 | 职责 |
| --- | --- |
| `scripts/native.ps1` | 在 Windows PowerShell 5.1 中可靠传播原生命令退出码 |
| `scripts/env.ps1` | 加载本地 `.env`，且不覆盖当前进程已显式设置的环境变量 |
| `scripts/build.ps1` | 构建 API 和 Worker Windows 二进制 |
| `scripts/dev.ps1` | 加载本地配置，等待 PostgreSQL 后启动 API 与 Worker |
| `scripts/check.ps1` | 运行格式、测试、vet、构建和 Compose 检查 |
| `cmd/rag-eval/main.go` | 实际执行 Eino Graph 并生成 JSON/Markdown 安全评估报告 |
| `evals/cases/rag_mvp.json` | 版本化 RAG MVP 决策、引用和模型调用评估集 |
| `evals/runner/test_rag_mvp.py` | 通过 pytest 执行 Eval 并校验发布门槛 |
| `.github/workflows/ci.yml` | 运行 Go、PostgreSQL、pytest Eval、构建和 Compose 检查 |

## 产品与设计文档

| 文件 | 职责 |
| --- | --- |
| `docs/PRODUCT_BRIEF.md` | 产品定位、目标用户和价值主张 |
| `docs/REQUIREMENTS.md` | MVP 功能需求和验收标准 |
| `docs/USER_FLOWS.md` | 客户、客服和管理员关键流程 |
| `docs/ARCHITECTURE.md` | 系统分层、依赖方向和运行架构 |
| `docs/EVALUATION.md` | RAG、Agent 和安全能力的评估方案 |
| `docs/ROADMAP.md` | 项目阶段和交付顺序 |
| `docs/CODEBASE.md` | 当前代码目录和文件职责导航 |

## 不应提交的目录

以下目录是本地生成内容，已通过 `.gitignore` 排除：

- `.gocache/`
- `.gomodcache/`
- `.gotmp/`
- `bin/`
- `.idea/`
- `.vscode/`
- `web/node_modules/`
- `web/.next/`
