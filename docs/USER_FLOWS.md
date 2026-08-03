# Agent Chat 用户流程

## 1. 流程原则

- 知识问答优先使用可验证的企业知识。
- 不确定时追问或拒答，而不是补全未知事实。
- 读操作可以在授权范围内自动执行。
- 写操作必须先展示将要执行的内容并等待确认。
- 人工接管后，AI 默认停止自动回复。
- 每条链路必须留下可检索的 Trace。

## 2. 知识问答

```mermaid
flowchart TD
    A[客户发送问题] --> B[创建消息与 Agent Run]
    B --> C[理解问题和意图]
    C --> D[检索知识]
    D --> E[Answerability Gate]
    E -->|可回答| F[生成受知识约束的回答]
    F --> G[校验引用]
    G --> H[流式返回回答与引用]
    E -->|需要澄清| I[向客户提出澄清问题]
    E -->|不可回答| J[拒答并建议人工支持]
    H --> K[保存 Trace 与反馈入口]
    I --> K
    J --> K
```

关键状态：

- `run.started`
- `retrieval.completed`
- `answerability.decided`
- `generation.streaming`
- `run.completed`
- `run.failed`

## 3. 查询订阅信息

```mermaid
sequenceDiagram
    participant U as 客户
    participant API as API
    participant E as Eino Agent
    participant CRM as 模拟 CRM

    U->>API: 我的套餐包含多少调用量？
    API->>E: 启动 Agent Run
    E->>E: 识别为订阅查询
    E->>CRM: query_subscription(customer_id)
    CRM-->>E: 套餐与额度
    E-->>API: 生成解释性回答
    API-->>U: 流式回答和工具执行状态
```

约束：

- 只能查询当前会话关联客户。
- Agent 不能自行构造其他客户 ID。
- CRM 不可用时，不得猜测订阅状态。

## 4. 创建技术支持工单

```mermaid
sequenceDiagram
    participant U as 客户
    participant API as API
    participant E as Eino Graph
    participant DB as PostgreSQL
    participant W as Worker

    U->>API: 这个问题没解决，帮我建工单
    API->>E: 启动创建工单流程
    E->>E: 汇总问题并生成工单草稿
    E->>DB: 原子保存 Run 结果、草稿和待确认请求
    E-->>U: 展示标题、描述和优先级，请求确认
    U->>API: 确认
    API->>DB: 原子更新确认状态并创建 ticket.create Job
    W->>DB: 领取 Job，以审批 ID 幂等创建工单
    DB-->>U: 查询状态时返回工单编号
```

取消路径：

1. 用户回复取消。
2. 系统将确认请求标记为 `cancelled`。
3. 工作流结束。
4. 工单服务不得被调用。

异常路径：

- 重复确认：返回第一次执行结果。
- 确认过期：提示重新发起。
- 工单服务超时：任务进入可重试状态，不重复建单。

## 5. 转人工

```mermaid
flowchart TD
    A[客户主动要求或策略触发] --> B[检查当前会话状态]
    B --> C[生成结构化接管摘要]
    C --> D[保存转人工事件]
    D --> E[会话进入等待人工]
    E --> F[通知客服工作台]
    F --> G[客服查看摘要与历史]
    G --> H[客服接管]
    H --> I[AI 停止自动回复]
    I --> J[客服解决或创建工单]
```

接管摘要至少包含：

- 客户当前诉求
- 已确认事实
- 未解决问题
- 风险信号
- 已引用知识
- 已调用工具及结果
- 推荐下一步

## 6. 知识导入与索引

```mermaid
flowchart LR
    A[管理员创建文档] --> B[保存文档版本]
    B --> C[创建持久化索引任务]
    C --> D[解析与清洗]
    D --> E[切片]
    E --> F[生成 Embedding]
    F --> G[写入 pgvector]
    G --> H[原子切换活动版本]
    H --> I[标记索引成功]
    C -->|失败| J[记录错误并按策略重试]
```

更新文档时：

- 新版本索引完成前，旧版本继续服务。
- 新版本完成后，原子切换活动版本。
- 旧向量异步清理，不得与新版本混合返回。

## 7. 人工接管后的状态

会话状态建议：

```text
ai_active
  -> waiting_human
  -> human_active
  -> resolved
```

允许的特殊转换：

```text
human_active -> ai_active
```

只有客服明确恢复 AI 后才能发生该转换。

## 8. Trace 查看

管理员从一次会话进入运行详情，应能按顺序看到：

1. 用户消息和运行配置版本。
2. Eino Graph 节点路径。
3. 检索查询、命中、分数和使用情况。
4. Answerability 结果和理由。
5. 模型调用与 Token。
6. 工具输入、输出、耗时和错误。
7. 审批请求、状态事件和持久化 Job 执行结果。
8. 最终回答、引用与结束状态。
