import { AlertTriangle, Info, Loader2 } from "lucide-react";
import Link from "next/link";

import { Button } from "@/components/ui/button";
import { CitationList } from "@/components/citation-list";
import { TicketApprovalCard } from "@/components/ticket-approval-card";
import type { AssistantState, RunStage } from "@/lib/types";

/**
 * 运行详情入口。
 *
 * 这是演示便利而非产品特性：客户不应看到内部 Trace。链接独立于消息内容渲染，
 * 移除时只需删除本组件的调用点。
 */
function TraceLink({ runId }: { runId: string }) {
  return (
    <Link
      href={`/admin/runs/${encodeURIComponent(runId)}`}
      className="mt-2 inline-block text-xs text-muted-foreground underline underline-offset-4"
    >
      查看运行详情（内部视图）
    </Link>
  );
}

const STAGE_LABEL: Record<RunStage, string> = {
  pending: "正在排队…",
  retrieving: "正在检索知识…",
  deciding: "正在判断可回答性…",
  generating: "正在生成回答…",
  completed: "",
  failed: "",
};

/** 处理进度由 SSE 事件驱动，让后端各节点的执行过程对用户可见。 */
function RunProgress({ stage }: { stage: RunStage }) {
  return (
    <p className="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 className="size-3.5 animate-spin" aria-hidden />
      {STAGE_LABEL[stage]}
    </p>
  );
}

/**
 * 三个分支必须视觉可区分，否则 Answerability 的判定对用户不可见，
 * 「澄清」会被误读成一个答得很差的回答。
 */
function DecisionNotice({ state, onRequestHuman }: { state: AssistantState; onRequestHuman?: () => void }) {
  const clarification = state.decision === "needs_clarification";
  const Icon = clarification ? Info : AlertTriangle;

  return (
    <div
      className={
        clarification
          ? "rounded-lg border border-border bg-muted/50 p-3"
          : "rounded-lg border border-destructive/30 bg-destructive/5 p-3"
      }
    >
      <p
        className={
          clarification
            ? "flex items-center gap-2 text-sm font-medium"
            : "flex items-center gap-2 text-sm font-medium text-destructive"
        }
      >
        <Icon className="size-4" aria-hidden />
        {clarification ? "需要补充信息" : "知识库暂无相关信息"}
      </p>
      <p className="mt-2 text-sm whitespace-pre-wrap">{state.answer}</p>
      {state.nextAction === "request_human_support" ? (
        <div className="mt-3 flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={onRequestHuman} disabled={!onRequestHuman}>
            联系人工支持
          </Button>
        </div>
      ) : null}
    </div>
  );
}

export function AssistantMessage({ state, onRequestHuman }: { state: AssistantState; onRequestHuman?: () => void }) {
  if (state.stage === "failed") {
    return (
      <div className="max-w-[85%] rounded-lg border border-destructive/30 bg-destructive/5 p-3">
        <p className="flex items-center gap-2 text-sm font-medium text-destructive">
          <AlertTriangle className="size-4" aria-hidden />
          回答失败
        </p>
        <p className="mt-1 font-mono text-xs text-muted-foreground">
          {state.errorCode}
        </p>
        <TraceLink runId={state.runId} />
      </div>
    );
  }

  if (state.stage !== "completed") {
    return (
      <div className="max-w-[85%] rounded-lg border border-border bg-card p-3">
        <RunProgress stage={state.stage} />
      </div>
    );
  }

  if (state.decision && state.decision !== "answerable") {
    return (
      <div className="max-w-[85%]">
        <DecisionNotice state={state} onRequestHuman={onRequestHuman} />
        <TraceLink runId={state.runId} />
      </div>
    );
  }

  return (
    <div className="max-w-[85%]">
      <div className="rounded-lg border border-border bg-card p-3">
        <p className="text-sm whitespace-pre-wrap">{state.answer}</p>
        <CitationList citations={state.citations} />
        {state.approval ? <TicketApprovalCard prompt={state.approval} /> : null}
      </div>
      <TraceLink runId={state.runId} />
    </div>
  );
}
