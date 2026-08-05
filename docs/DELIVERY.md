# Agent Chat MVP 交付与演示

## 1. 交付目标

本指南用于在新环境中 15 分钟内启动可公开演示的 Agent Chat，并留下可复现的测试、
Eval 和性能证据。演示使用虚构 SaaS 数据，不应导入真实客户资料。

## 2. 前置条件

- Docker Desktop 或 Docker Engine，支持 Docker Compose v2。
- 可用的 DeepSeek API Key 和智谱 Embedding API Key。
- 本机端口 `3000`、`8080`、`5433` 未被占用。

端口冲突时可在 `.env` 覆盖 `WEB_PORT`、`API_PORT` 和 `POSTGRES_PORT`。

复制 `.env.example` 为 `.env`，只填写：

```dotenv
LLM_API_KEY=<your-deepseek-key>
EMBEDDING_API_KEY=<your-zhipu-key>
```

`.env` 已被 Git 忽略。不要把 Key 写入命令历史、截图、日志或录屏。

## 3. 一键启动

```bash
docker compose up -d --build --wait
docker compose ps
docker compose logs demo-seed
```

预期结果：

- `postgres`、`api`、`worker` 和 `web` 为 running/healthy。
- `demo-seed` 以退出码 0 完成，并输出 `knowledgeBaseId` 与 `faqImportId`。
- http://127.0.0.1:8080/readyz 返回 200。
- http://127.0.0.1:3000 可以打开客户聊天。

`demo-seed` 只通过管理 API 创建知识库和导入 FAQ，重复执行会复用同名知识库和内容相同
的导入记录。Web 在服务端按唯一名称解析该知识库；缺失或重名时拒绝创建客户会话。

进入 http://127.0.0.1:3000/admin/knowledge，等待 10 行 FAQ 全部变为“已就绪”。
若一直等待，检查 Worker 是否因为缺少 `EMBEDDING_API_KEY` 而禁用了 `knowledge.index`。

## 4. 八步演示脚本

1. **知识导入**：在知识管理页展示已自动导入的 10 条 SaaS FAQ 和逐行索引状态。
2. **带引用回答**：客户聊天询问“如何重置密码？”，展开引用并跳转 Run Trace。
3. **安全降级**：询问缺少错误码的模糊问题，展示 `needs_clarification` 追问。
4. **订阅工具**：询问“我本月还剩多少 API 调用？”，展示当前客户订阅与工具 Trace。
5. **写操作审批**：多轮描述故障后说“帮我建个工单”，展示草稿；确认后展示工单编号。
6. **未确认无副作用**：再次生成草稿并取消，说明取消、过期和重复确认由数据库约束保护。
7. **人工接管**：客户点击转人工，在 `/agent` 接管、回复，再明确恢复 AI。
8. **Trace 与 Eval**：展示 Run 节点、检索、Token、工具和审批事件，再运行固定 Eval。

## 5. 发布证据

```bash
go test ./...
go vet ./...
python -m pytest evals/runner
python -m unittest discover -s demo -p "test_*.py"
python -m unittest discover -s scripts -p "test_*.py"
cd web && npm ci && npm test && npx tsc --noEmit && npm run lint && npm run build
docker compose config --quiet
```

真实 PostgreSQL 集成测试：

```bash
TEST_DATABASE_URL=postgres://agent_chat:agent_chat_password@127.0.0.1:5433/agent_chat?sslmode=disable go test -count=1 ./...
```

生成非模型 API 性能报告：

```bash
python scripts/benchmark.py
```

输出为 `docs/reports/performance.json` 和 `docs/reports/performance.md`。脚本对健康、就绪和
知识库列表分别预热并采样 100 次，任一端点非 200 或 P95 不低于 300ms 都返回失败。

## 6. 录屏与截图

发布前录制 3～5 分钟视频，按第 4 节顺序演示，并至少保留以下截图：

- 带引用回答与 Run Trace。
- 工单草稿确认界面。
- 人工接管摘要和客服工作台。
- 60 Case Eval 报告和性能报告。

录制前清空浏览器通知、终端历史和 `.env`，检查画面中没有 API Key、Authorization、
本机用户名或真实客户数据。视频或在线演示 URL 应在发布 PR 中补充；仓库不提交大型视频。

## 7. 故障排查与清理

```bash
docker compose logs api worker demo-seed web
docker compose restart api worker web
```

需要完全重建演示数据时，以下命令会删除本项目 Compose 的 PostgreSQL 数据卷：

```bash
docker compose down -v
docker compose up -d --build --wait
```

不要在含真实数据的环境执行数据卷删除命令。

## 8. 简历项目描述

> 独立设计并实现企业级 AI 技术支持平台，使用 Go、Eino、PostgreSQL/pgvector 与 Next.js
> 构建带引用 RAG、三分支 Answerability、客户作用域工具、持久化写操作审批、幂等 Job、
> 人工接管和可恢复 SSE；建立 60 条版本化 Eval 与 100% 安全硬门槛，并通过 Docker Compose
> 实现演示数据自动初始化和一键部署。
