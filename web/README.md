# Agent Chat Web

客户聊天页：向企业知识库提问，回答附带可核对的引用来源。

## 架构约定

浏览器只与 Next.js 路由处理器通信，由后者转发到 Go API 并注入演示身份头。
这样后端无需开启 CORS，演示身份也不会出现在客户端代码里。

```
浏览器 ──▶ Next.js 路由处理器 ──▶ Go API
           （注入 X-Customer-ID）
```

对应的四个转发端点：

| 前端路由 | 后端端点 |
| --- | --- |
| `POST /api/conversations` | `POST /api/v1/conversations` |
| `GET /api/conversations/{id}/messages` | 同名历史分页端点 |
| `POST /api/conversations/{id}/messages` | 同名客户端点 |
| `GET /api/agent-runs/{runId}/events` | 同名 SSE 端点（流式透传） |

## 本地运行

先按仓库根目录说明启动 PostgreSQL、Go API 和 Worker，然后：

```bash
cp .env.example .env.local   # 填入 KNOWLEDGE_BASE_ID
npm install
npm run dev
```

`KNOWLEDGE_BASE_ID` 必填：后端目前没有「列出知识库」的接口，会话绑定的知识库
只能由配置指定。可通过 `POST /api/v1/admin/knowledge-bases` 创建后获得 ID。

## 当前限制

- 知识库由配置绑定，页面不提供选择器。
- 回答一次性返回。SSE 事件契约已按增量设计，后端接入流式生成后无需改动前端。

浏览器将当前会话 ID 保存到 `localStorage`。刷新时只读取历史；若最后一个 Run 仍为
`pending` 或 `running`，页面恢复原 Run 的 SSE 订阅，不会重新发送消息或创建第二个 Run。
