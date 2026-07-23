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

## Knowledge Persistence

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/persistence/knowledge/doc.go` | Knowledge PostgreSQL Repository 包说明 |
| `internal/infrastructure/persistence/knowledge/repository.go` | 实现版本与 Job 原子创建、切片替换、发布和 pgvector 检索 |
| `internal/infrastructure/persistence/knowledge/repository_integration_test.go` | 使用真实 PostgreSQL 验证版本生命周期、原子回滚和活动版本检索 |

## Worker

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/jobs/doc.go` | Jobs 包职责说明 |
| `internal/infrastructure/jobs/job.go` | 定义 Job、Handler、稳定错误分类和幂等执行契约 |
| `internal/infrastructure/jobs/queue.go` | 使用 PostgreSQL 租约原子领取、完成、重试和恢复任务 |
| `internal/infrastructure/jobs/worker.go` | 管理类型分发、执行超时、有界退避和 Context 生命周期 |
| `internal/infrastructure/jobs/knowledge_handler.go` | 校验 `knowledge.index` Payload 并适配 Application 索引用例 |
| `internal/infrastructure/jobs/worker_test.go` | 验证成功、重试、永久失败、取消收尾和类型过滤 |
| `internal/infrastructure/jobs/knowledge_handler_test.go` | 验证索引 Payload、幂等键和失败分类映射 |
| `internal/infrastructure/jobs/queue_integration_test.go` | 使用真实 PostgreSQL 验证并发领取、租约、重试上限和锁恢复 |

Worker 只领取 Bootstrap 已注册的 Job 类型。开发环境缺少 `EMBEDDING_API_KEY` 时不注册 `knowledge.index` Handler，已有索引任务保持 `pending`。

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

已经提交或执行的迁移文件不可直接改写；后续 Schema 变化必须新增版本文件。

## 模型 Provider

| 文件 | 职责 |
| --- | --- |
| `internal/infrastructure/model/doc.go` | Model Provider 包职责说明 |
| `internal/infrastructure/model/deepseek.go` | 将 DeepSeek OpenAI 兼容接口适配为 Eino ToolCallingChatModel |
| `internal/infrastructure/model/deepseek_test.go` | 验证模型、鉴权和 thinking 参数请求 |
| `internal/infrastructure/model/zhipu.go` | 将智谱 Embeddings API 适配为固定模型和维度的 Eino Embedder |
| `internal/infrastructure/model/zhipu_test.go` | 验证批量向量顺序、模型覆盖保护和错误脱敏 |

## 开发脚本与 CI

| 文件 | 职责 |
| --- | --- |
| `scripts/native.ps1` | 在 Windows PowerShell 5.1 中可靠传播原生命令退出码 |
| `scripts/build.ps1` | 构建 API 和 Worker Windows 二进制 |
| `scripts/dev.ps1` | 等待 PostgreSQL 后启动 API 与 Worker |
| `scripts/check.ps1` | 运行格式、测试、vet、构建和 Compose 检查 |
| `.github/workflows/ci.yml` | 在 GitHub Actions 中运行单元测试、真实数据库集成测试和构建 |

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
