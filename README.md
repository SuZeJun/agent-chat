# Agent Chat

面向企业客服与技术支持团队的 AI 服务运营平台。项目当前处于工程脚手架阶段，产品和架构设计见 `docs/`，代码入口和文件职责见 `docs/CODEBASE.md`。

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
- 带 advisory lock、文件名和 SHA-256 校验的事务迁移
- `/healthz` 与 `/readyz`
- 结构化日志、服务端请求 ID、受控 panic 恢复和优雅退出
- 带状态约束、重试约束和部分索引的 Job 基础表
- Go 单元测试、PostgreSQL 集成测试和 GitHub Actions

Worker 只领取已注册的 Job 类型，避免新旧版本部署期间误消费尚未支持的任务。配置 `EMBEDDING_API_KEY` 后会注册 `knowledge.index` Handler；开发环境未配置 Key 时 Worker 仍可启动，但索引能力会明确记录为 disabled，任务保持 `pending`。

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

默认配置与 Compose 一致。覆盖配置时参考 `.env.example` 设置环境变量；应用不会自动读取 `.env` 文件。模型默认使用关闭 thinking 的 `deepseek-v4-flash` 与 1024 维 `embedding-3`，API Key 不得提交到仓库。API 健康检查不调用模型；开发环境未配置 embedding Key 时 Worker 不注册索引 Handler。`production` 环境必须显式配置 `DATABASE_URL`、`LLM_API_KEY` 和 `EMBEDDING_API_KEY`，模型端点必须使用 HTTPS；数据库 `sslmode` 仅接受 `require`、`verify-ca` 或 `verify-full`，生产部署优先使用 `verify-full`。

Worker 默认单进程串行执行任务。`WORKER_JOB_TIMEOUT` 限制单次执行时间，`WORKER_LOCK_TIMEOUT` 必须更长，用于恢复进程崩溃遗留的租约；重试使用 `WORKER_RETRY_BASE_DELAY` 到 `WORKER_RETRY_MAX_DELAY` 之间的有界指数退避。

## Windows PowerShell

原生 Windows 推荐使用：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\dev.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\build.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\check.ps1
```

`Makefile` 使用 POSIX shell，支持 Linux、macOS、WSL 和 Git Bash，不保证在原生 CMD 环境运行：

```bash
make deps
make check
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
docker compose config --quiet
```

真实迁移集成测试需要设置 `TEST_DATABASE_URL`；CI 和 `scripts/check.ps1` 会自动执行。若使用过未带迁移校验和的旧版开发数据库，可执行 `docker compose down -v` 后重建本地数据卷。

## 下一阶段

下一阶段实现第一条 RAG 纵向闭环：

```text
FAQ 导入与索引（已完成）
  -> Eino Retriever（已完成）
  -> Eino RAG Graph
  -> Answerability Gate
  -> 带引用流式回答
  -> Trace
```
