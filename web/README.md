# Agent Chat Web

客户聊天页：向企业知识库提问，回答附带可核对的引用来源。

## 架构约定

浏览器只与 Next.js 路由处理器通信，由后者转发到 Go API 并注入演示身份头。
这样后端无需开启 CORS，演示身份也不会出现在客户端代码里。

```
浏览器 ──▶ Next.js 路由处理器 ──▶ Go API
           （注入客户、客服或管理员身份）
```

主要转发端点：

| 前端路由 | 后端端点 |
| --- | --- |
| `GET /api/admin/knowledge-bases` | 管理员知识库列表 |
| `POST /api/admin/knowledge-bases/{id}/faq-imports` | FAQ CSV 导入 |
| `GET /api/admin/knowledge-bases/{id}/faq-imports/{importId}` | 逐行索引状态 |
| `POST /api/conversations` | `POST /api/v1/conversations` |
| `GET /api/conversations/{id}/messages` | 同名历史分页端点 |
| `POST /api/conversations/{id}/messages` | 同名客户端点 |
| `GET /api/agent-runs/{runId}/events` | 同名 SSE 端点（流式透传） |
| `GET /api/ticket-approvals/{approvalId}` | 查询客户所属审批与工单状态 |
| `POST /api/ticket-approvals/{approvalId}/confirm` | 确认草稿并持久化创建任务 |
| `POST /api/ticket-approvals/{approvalId}/cancel` | 取消草稿，不产生工单副作用 |
| `POST /api/conversations/{id}/handoff` | 客户主动转人工并持久化摘要 |
| `POST /api/conversations/{id}/handoff/messages` | 人工阶段客户消息 |
| `GET /api/conversations/{id}/events` | 客户作用域会话事件增量页 |
| `GET /api/agent/conversations` | 等待队列与当前客服会话 |
| `POST /api/agent/conversations/{id}/takeover` | 原子接管会话 |
| `POST /api/agent/conversations/{id}/messages` | 当前客服回复 |
| `POST /api/agent/conversations/{id}/resume-ai` | 当前客服恢复 AI |

## 本地运行

先按仓库根目录说明启动 PostgreSQL、Go API 和 Worker，然后：

```bash
cp .env.example .env.local   # 填入 KNOWLEDGE_BASE_ID
npm install
npm run dev
```

客户聊天仍要求 `KNOWLEDGE_BASE_ID`，资源作用域只能由服务端配置指定。管理员可访问
`/admin/knowledge` 列出知识库、上传 FAQ CSV 并观察每行索引状态；客服使用
`DEMO_AGENT_ID` 绑定演示身份，并从 `/agent` 进入人工接管工作台。

## 当前限制

- 客户聊天知识库由配置绑定；仅管理员知识页面提供知识库切换。
- 回答一次性返回。SSE 事件契约已按增量设计，后端接入流式生成后无需改动前端。
- 人工队列和会话事件当前每 2 秒读取持久化增量；WebSocket 推送仍属于后续部署增强。

浏览器按知识库将当前会话 ID 保存到 `localStorage`。刷新时只读取历史；若存在
`pending` 或 `running` Run，页面按 `runId` 恢复每个 SSE 订阅，不会重新发送消息或创建新 Run。
工单草稿从服务端历史恢复，确认与取消均以审批 API 的权威状态为准；确认后页面查询
持久化 Job 的执行结果，只有服务端返回工单记录时才展示工单编号。
