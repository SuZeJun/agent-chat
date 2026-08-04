import { CitationList } from "@/components/citation-list";
import type { RunEvent, RunTrace, RunTraceStep, ToolCall } from "@/lib/types";

const DECISION_LABEL: Record<string, string> = {
  answerable: "可回答",
  needs_clarification: "需要澄清",
  unanswerable: "不可回答",
};

const NEXT_ACTION_LABEL: Record<string, string> = {
  provide_details: "请求补充信息",
  request_human_support: "建议转人工",
  confirm_ticket: "等待确认工单草稿",
};

const APPROVAL_EVENT_LABEL: Partial<Record<RunEvent["type"], string>> = {
  "approval.required": "等待客户确认",
  "approval.confirmed": "客户已确认",
  "approval.cancelled": "客户已取消",
  "approval.expired": "确认已过期",
  "ticket.created": "工单已创建",
};

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="rounded-lg border border-border p-4">
      <h2 className="text-sm font-semibold">{title}</h2>
      {hint ? <p className="mt-0.5 text-xs text-muted-foreground">{hint}</p> : null}
      <div className="mt-3">{children}</div>
    </section>
  );
}

/** 阶段 2 前工具与 Interrupt 尚未产生数据，明确标注空态而不是隐藏整节。 */
function EmptyState({ children }: { children: React.ReactNode }) {
  return <p className="text-xs text-muted-foreground">{children}</p>;
}

function NodePath({ path }: { path: string[] }) {
  if (path.length === 0) {
    return <EmptyState>本次运行没有记录节点路径。</EmptyState>;
  }
  return (
    <ol className="flex flex-wrap items-center gap-1.5 text-xs">
      {path.map((node, index) => (
        <li key={`${node}-${index}`} className="flex items-center gap-1.5">
          <span className="rounded-md border border-border px-2 py-1 font-mono">{node}</span>
          {index < path.length - 1 ? (
            <span className="text-muted-foreground">→</span>
          ) : null}
        </li>
      ))}
    </ol>
  );
}

function StepTable({ steps }: { steps: RunTraceStep[] }) {
  if (steps.length === 0) {
    return <EmptyState>本次运行没有记录执行步骤。</EmptyState>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <thead className="text-muted-foreground">
          <tr className="border-b border-border text-left">
            <th className="py-1.5 pr-3 font-medium">#</th>
            <th className="py-1.5 pr-3 font-medium">节点</th>
            <th className="py-1.5 pr-3 font-medium">组件</th>
            <th className="py-1.5 pr-3 font-medium">状态</th>
            <th className="py-1.5 pr-3 text-right font-medium">耗时</th>
            <th className="py-1.5 pr-3 text-right font-medium">Prompt</th>
            <th className="py-1.5 text-right font-medium">Completion</th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {steps.map((step) => (
            <tr key={step.order} className="border-b border-border/50 last:border-0">
              <td className="py-1.5 pr-3 text-muted-foreground">{step.order}</td>
              <td className="py-1.5 pr-3">{step.name}</td>
              <td className="py-1.5 pr-3 text-muted-foreground">
                {step.componentType ? `${step.component}/${step.componentType}` : step.component}
              </td>
              <td className="py-1.5 pr-3">{step.status}</td>
              <td className="py-1.5 pr-3 text-right">{step.durationMillis} ms</td>
              <td className="py-1.5 pr-3 text-right">{step.promptTokens || "—"}</td>
              <td className="py-1.5 text-right">{step.completionTokens || "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ToolCallTable({ calls }: { calls: ToolCall[] }) {
  if (calls.length === 0) {
    return <EmptyState>本次运行没有调用工具。</EmptyState>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <thead className="text-muted-foreground">
          <tr className="border-b border-border text-left">
            <th className="py-1.5 pr-3 font-medium">工具</th>
            <th className="py-1.5 pr-3 font-medium">状态</th>
            <th className="py-1.5 pr-3 font-medium">错误码</th>
            <th className="py-1.5 text-right font-medium">耗时</th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {calls.map((call, index) => (
            <tr key={`${call.name}-${index}`} className="border-b border-border/50 last:border-0">
              <td className="py-1.5 pr-3">{call.name}</td>
              <td className="py-1.5 pr-3">{call.status}</td>
              <td className="py-1.5 pr-3 text-muted-foreground">
                {call.errorCode ?? "—"}
              </td>
              <td className="py-1.5 text-right">{call.durationMillis} ms</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ApprovalTimeline({ events }: { events: RunEvent[] }) {
  const approvalEvents = events.filter((event) => APPROVAL_EVENT_LABEL[event.type]);
  if (approvalEvents.length === 0) {
    return <EmptyState>本次运行没有人工确认记录。</EmptyState>;
  }
  return (
    <ol className="space-y-2 text-xs">
      {approvalEvents.map((event) => {
        const approvalId = String(event.payload.approvalId ?? "");
        const ticketNumber = String(event.payload.ticketNumber ?? "");
        const jobId = String(event.payload.jobId ?? "");
        return (
          <li key={event.eventId} className="rounded-md border border-border p-2">
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className="font-medium">{APPROVAL_EVENT_LABEL[event.type]}</span>
              <span className="font-mono text-muted-foreground">#{event.sequence}</span>
              <time className="ml-auto font-mono text-muted-foreground">
                {event.createdAt}
              </time>
            </div>
            <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 font-mono text-muted-foreground">
              {approvalId ? <span>approval={approvalId}</span> : null}
              {jobId ? <span>job={jobId}</span> : null}
              {ticketNumber ? <span>ticket={ticketNumber}</span> : null}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

export function RunTraceView({ trace }: { trace: RunTrace }) {
  const assessment = trace.result.assessment;
  const evidence = assessment?.evidence ?? [];
  const totalPromptTokens = trace.steps.reduce((sum, step) => sum + step.promptTokens, 0);
  const totalCompletionTokens = trace.steps.reduce(
    (sum, step) => sum + step.completionTokens,
    0,
  );

  return (
    <div className="mx-auto max-w-4xl space-y-4 p-4">
      <header className="rounded-lg border border-border p-4">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <h1 className="text-sm font-semibold">运行详情</h1>
          <span className="font-mono text-xs text-muted-foreground">{trace.runId}</span>
          <span
            className={
              trace.status === "failed"
                ? "ml-auto rounded-md border border-destructive/30 bg-destructive/5 px-2 py-0.5 text-xs text-destructive"
                : "ml-auto rounded-md border border-border px-2 py-0.5 text-xs"
            }
          >
            {trace.status}
            {trace.errorCode ? `：${trace.errorCode}` : ""}
          </span>
        </div>
        <dl className="mt-3 grid gap-x-6 gap-y-1 text-xs sm:grid-cols-2">
          <div className="flex gap-2">
            <dt className="text-muted-foreground">会话</dt>
            <dd className="font-mono">{trace.conversationId}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-muted-foreground">请求</dt>
            <dd className="font-mono">{trace.requestId}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-muted-foreground">创建</dt>
            <dd className="font-mono">{trace.createdAt}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-muted-foreground">结束</dt>
            <dd className="font-mono">{trace.completedAt ?? "—"}</dd>
          </div>
        </dl>
      </header>

      <Section title="1. 用户消息与运行配置">
        <p className="text-sm whitespace-pre-wrap">{trace.question}</p>
        <p className="mt-3 text-xs text-muted-foreground">
          运行配置版本尚未随 Run 记录；当前只能从执行步骤中读到模型身份。
        </p>
      </Section>

      <Section title="2. Graph 节点路径">
        <NodePath path={trace.result.nodePath ?? []} />
      </Section>

      <Section
        title="3. 检索命中与分数"
        hint="分数为查询与切片的向量余弦相似度，降序排列。"
      >
        {evidence.length === 0 ? (
          <EmptyState>本次运行没有检索到任何证据。</EmptyState>
        ) : (
          <ul className="space-y-1.5 text-xs">
            {evidence.map((item) => (
              <li key={item.sourceId} className="flex items-baseline gap-2">
                <span className="shrink-0 font-mono text-muted-foreground">
                  [{item.sourceId}]
                </span>
                <span>{item.title}</span>
                <span className="ml-auto shrink-0 font-mono text-muted-foreground">
                  {item.score.toFixed(4)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="4. 可回答性判定">
        {assessment?.decision ? (
          <dl className="grid gap-x-6 gap-y-1 text-xs sm:grid-cols-2">
            <div className="flex gap-2">
              <dt className="text-muted-foreground">决策</dt>
              <dd className="font-medium">
                {DECISION_LABEL[assessment.decision] ?? assessment.decision}
              </dd>
            </div>
            <div className="flex gap-2">
              <dt className="text-muted-foreground">理由</dt>
              <dd className="font-mono">{assessment.reason ?? "—"}</dd>
            </div>
            <div className="flex gap-2">
              <dt className="text-muted-foreground">置信度</dt>
              <dd className="font-mono">{assessment.confidence?.toFixed(4) ?? "—"}</dd>
            </div>
            <div className="flex gap-2">
              <dt className="text-muted-foreground">下一步</dt>
              <dd>
                {trace.result.nextAction
                  ? (NEXT_ACTION_LABEL[trace.result.nextAction] ?? trace.result.nextAction)
                  : "—"}
              </dd>
            </div>
          </dl>
        ) : (
          <EmptyState>本次运行没有产生判定结果。</EmptyState>
        )}
      </Section>

      <Section
        title="5. 模型调用与 Token"
        hint={`合计 Prompt ${totalPromptTokens}，Completion ${totalCompletionTokens}。`}
      >
        <StepTable steps={trace.steps} />
      </Section>

      <Section title="6. 工具调用">
        <ToolCallTable calls={trace.result.toolCalls ?? []} />
      </Section>

      <Section title="7. Interrupt、Checkpoint 与 Resume">
        <ApprovalTimeline events={trace.events ?? []} />
      </Section>

      <Section title="8. 最终回答与引用">
        {trace.result.answer ? (
          <>
            <p className="text-sm whitespace-pre-wrap">{trace.result.answer}</p>
            <CitationList citations={trace.result.citations ?? []} />
          </>
        ) : (
          <EmptyState>本次运行没有产生回答。</EmptyState>
        )}
      </Section>
    </div>
  );
}
