# Agent Chat

面向企业客服与技术支持团队的 AI 服务运营平台。当前已完成 FAQ RAG 与安全工单审批闭环，产品和架构设计见 `docs/`，代码入口和文件职责见 `docs/CODEBASE.md`。

## 当前能力

- Go API 与 Worker 双进程入口
- PostgreSQL + pgvector
- DeepSeek V4 Flash Eino ChatModel 适配
- 智谱 Embedding-3 Eino Embedder 适配
- FAQ/Markdown 逻辑文档、不可变版本和活动版本原子切换
- FAQ 原子问答切片、Markdown 结构块切片和超长内容重叠窗口
- 固定 1024 维的知识切片与 pgvector HNSW cosine 索引
- 文档版本与 `knowledge.index` Job 的事务性创建
- 基于 `FOR UPDATE SKIP LOCKED` 的持久化 Job 领取、有界重试和锁超时恢复
- `knowledge.index` Handler 批量生成 embedding、写入切片并单调发布最新版本
- 服务端绑定知识库、支持 Top-K/阈值/元数据过滤的 Eino Retriever
- `agent.run` Handler 执行 RAG Graph，并原子保存 Assistant Message、Graph Result 和有序运行事件
- FAQ CSV 内容幂等导入、逐行索引状态查询和客户隔离的聊天/SSE API
- FAQ/Markdown 管理页、知识库切换、不可变版本、失败重试和索引状态自动刷新
- Eino Callback 节点、模型耗时和 Token Trace
- 客户作用域的订阅查询只读工具，以及只生成草稿的 `draft_ticket` 写工具
- 持久化工单审批、过期、确认/取消、幂等 `ticket.create` Job 与结构化确认界面
- Run Trace 中的真实工具调用、审批状态流转与工单创建事件
- 16 条实际执行 Eino Graph 的 pytest 离线安全评估，分数取自真实检索实测
- 带 advisory lock、文件名和 SHA-256 校验的事务迁移
- `/healthz` 与 `/readyz`
- 结构化日志、服务端请求 ID、受控 panic 恢复和优雅退出
- 带状态约束、重试约束和部分索引的 Job 基础表
- Go 单元测试、PostgreSQL 集成测试和 GitHub Actions

Worker 只领取已注册的 Job 类型，避免新旧版本部署期间误消费尚未支持的任务。配置 `EMBEDDING_API_KEY` 后会注册 `knowledge.index` Handler；同时配置 `LLM_API_KEY` 和 `EMBEDDING_API_KEY` 后会注册 `agent.run` Handler。开发环境缺少相应 Key 时 Worker 仍可启动，但对应任务保持 `pending` 并记录 disabled 原因。

## 本地启动

启动并等待 PostgreSQL 健康：

```bash
docker compose up -d --wait postgres
```

分别启动 API 和 Worker：

```bash
go run ./cmd/api
go run ./cmd/worker
```

检查服务：

```bash
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

默认配置与 Compose 一致。覆盖配置时参考 `.env.example` 设置环境变量；应用本身不会自动读取 `.env`，Windows 本地开发脚本 `scripts/dev.ps1` 会自动加载仓库根目录的 `.env`，且不会覆盖当前进程中已显式设置的变量。模型默认使用关闭 thinking 的 `deepseek-v4-flash` 与 1024 维 `embedding-3`，API Key 不得提交到仓库。API 健康检查不调用模型；开发环境缺少模型 Key 时 Worker 不注册对应 Handler。`production` 环境必须显式配置 `DATABASE_URL`、`LLM_API_KEY` 和 `EMBEDDING_API_KEY`，模型端点必须使用 HTTPS；数据库 `sslmode` 仅接受 `require`、`verify-ca` 或 `verify-full`，生产部署优先使用 `verify-full`。

Worker 默认单进程串行执行任务。`WORKER_JOB_TIMEOUT` 限制单次执行时间，`WORKER_LOCK_TIMEOUT` 必须更长，用于恢复进程崩溃遗留的租约；重试使用 `WORKER_RETRY_BASE_DELAY` 到 `WORKER_RETRY_MAX_DELAY` 之间的有界指数退避。

## RAG 演示 API

MVP 使用 `X-Admin-ID` 和 `X-Customer-ID` 作为本地演示身份头；它们用于验证服务端资源绑定，不代表生产认证方案。

FAQ CSV 使用 UTF-8，支持以下两种表头，最多 1000 行、2 MiB：

```csv
question,answer
如何重置密码？,请在设置页选择重置密码。
```

```csv
question,answer,source_url
如何重置密码？,请在设置页选择重置密码。,https://docs.example.com/reset
```

主要接口：

```text
POST /api/v1/admin/knowledge-bases
GET  /api/v1/admin/knowledge-bases
POST /api/v1/admin/knowledge-bases/{knowledgeBaseId}/faq-imports
GET  /api/v1/admin/knowledge-bases/{knowledgeBaseId}/faq-imports/{importId}
GET  /api/v1/admin/knowledge-bases/{knowledgeBaseId}/documents
POST /api/v1/admin/knowledge-bases/{knowledgeBaseId}/documents
GET  /api/v1/admin/knowledge-bases/{knowledgeBaseId}/documents/{documentId}
POST /api/v1/admin/knowledge-bases/{knowledgeBaseId}/documents/{documentId}/versions
POST /api/v1/admin/knowledge-bases/{knowledgeBaseId}/documents/{documentId}/versions/{versionId}/retry
POST /api/v1/conversations
GET  /api/v1/conversations/{conversationId}/messages
POST /api/v1/conversations/{conversationId}/messages
GET  /api/v1/agent-runs/{runId}/events
GET  /api/v1/admin/agent-runs/{runId}
GET  /api/v1/ticket-approvals/{approvalId}
POST /api/v1/ticket-approvals/{approvalId}/confirm
POST /api/v1/ticket-approvals/{approvalId}/cancel
```

FAQ 导入接口使用 `multipart/form-data` 的 `file` 字段。同一知识库重复上传规范化内容相同的 CSV 会返回原 `importId`，不会重复创建文档、版本或 Job。Markdown 接口接受最大 512 KiB 的 UTF-8 内容；新版本与 `knowledge.index` Job 同事务写入，索引并发布成功前保留旧活动版本。会话历史接口按 `before` 游标分页，并返回 Assistant Message 关联的 Answerability、引用和 Run 状态快照。SSE 使用持久化 `sequence` 作为 `id`，客户端可通过 `Last-Event-ID` 断线续传。

## Windows PowerShell

原生 Windows 推荐使用：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\dev.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\build.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

`scripts/dev.ps1` 会自动加载本地 `.env`，同时启动 PostgreSQL、Worker 和 API；`.env` 已被 Git 忽略，不得提交。直接执行 `go run ./cmd/api` 或 `go run ./cmd/worker` 时仍需自行设置当前进程的环境变量。

`Makefile` 使用 POSIX shell，支持 Linux、macOS、WSL 和 Git Bash，不保证在原生 CMD 环境运行：

```bash
make deps
make check
make eval
make test-integration
make dev
```

## 验证

不依赖数据库的检查：

```bash
go test ./...
go vet ./...
go build ./cmd/api
go build ./cmd/worker
python -m pytest evals/runner
docker compose config --quiet
```

真实迁移集成测试需要设置 `TEST_DATABASE_URL`；CI 和 `scripts/check.ps1` 会自动执行。若使用过未带迁移校验和的旧版开发数据库，可执行 `docker compose down -v` 后重建本地数据卷。

## RAG 纵向闭环

```text
FAQ/Markdown 导入与索引（已完成）
  -> Eino Retriever（已完成）
  -> Eino RAG Graph（已完成）
  -> Answerability Gate（已完成）
  -> 带引用 SSE 回答（已完成）
  -> Trace 与 Eval（已完成）
  -> 只读工具（已完成）
  -> 工单草稿、人工确认与幂等执行（已完成）
```
