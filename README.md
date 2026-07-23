# Agent Chat

面向企业客服与技术支持团队的 AI 服务运营平台。项目当前处于工程脚手架阶段，产品和架构设计见 `docs/`，代码入口和文件职责见 `docs/CODEBASE.md`。

## 当前能力

- Go API 与 Worker 双进程入口
- PostgreSQL + pgvector
- 带 advisory lock、文件名和 SHA-256 校验的事务迁移
- `/healthz` 与 `/readyz`
- 结构化日志、服务端请求 ID、受控 panic 恢复和优雅退出
- 带状态约束、重试约束和部分索引的 Job 基础表
- Go 单元测试、PostgreSQL 集成测试和 GitHub Actions

当前 Worker 只验证进程生命周期和数据库连接，不领取或执行 Job；持久化任务消费属于后续里程碑。

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

默认配置与 Compose 一致。覆盖配置时参考 `.env.example` 设置环境变量；应用不会自动读取 `.env` 文件。`production` 环境必须显式配置 `DATABASE_URL`，且 `sslmode` 仅接受 `require`、`verify-ca` 或 `verify-full`，生产部署优先使用 `verify-full`。

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
FAQ 导入
  -> Embedding
  -> pgvector
  -> Eino Retriever
  -> Answerability Gate
  -> 带引用流式回答
  -> Trace
```
