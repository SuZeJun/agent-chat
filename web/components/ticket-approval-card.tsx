"use client";

import { AlertTriangle, CheckCircle2, Loader2, TicketCheck } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import type {
  ApiErrorBody,
  TicketApproval,
  TicketApprovalPrompt,
} from "@/lib/types";

const PRIORITY_LABEL: Record<TicketApprovalPrompt["draft"]["priority"], string> = {
  low: "低",
  normal: "普通",
  high: "高",
};

// 自动查询使用有限退避；耗尽后由客户手动刷新，避免 Worker 离线时永久轮询。
const TICKET_POLL_DELAYS_MS = [1_000, 1_500, 2_500, 4_000, 6_000, 8_000, 10_000, 10_000];

async function readApprovalError(response: Response): Promise<Error> {
  try {
    const body = (await response.json()) as ApiErrorBody;
    const error = new Error(
      body.error?.message || body.error?.code || `请求失败（${response.status}）`,
    );
    error.name = body.error?.code || "ticket_approval_failed";
    return error;
  } catch {
    return new Error(`请求失败（${response.status}）`);
  }
}

type ApprovalAction = "confirm" | "cancel";

export function TicketApprovalCard({ prompt }: { prompt: TicketApprovalPrompt }) {
  const [approval, setApproval] = useState<TicketApproval | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState<ApprovalAction | null>(null);
  const [pollingStopped, setPollingStopped] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const mountedRef = useRef(true);
  const requestSequenceRef = useRef(0);
  const refreshControllerRef = useRef<AbortController | null>(null);
  const endpoint = `/api/ticket-approvals/${encodeURIComponent(prompt.approvalId)}`;

  const refresh = useCallback(async () => {
    const sequence = ++requestSequenceRef.current;
    refreshControllerRef.current?.abort();
    const controller = new AbortController();
    refreshControllerRef.current = controller;
    try {
      const response = await fetch(endpoint, {
        cache: "no-store",
        signal: controller.signal,
      });
      if (!response.ok) {
        throw await readApprovalError(response);
      }
      const current = (await response.json()) as TicketApproval;
      if (
        !mountedRef.current ||
        controller.signal.aborted ||
        sequence !== requestSequenceRef.current
      ) {
        return null;
      }
      setApproval(current);
      setError(null);
      return current;
    } catch (cause) {
      if (controller.signal.aborted || sequence !== requestSequenceRef.current) {
        return null;
      }
      throw cause;
    } finally {
      if (refreshControllerRef.current === controller) {
        refreshControllerRef.current = null;
      }
    }
  }, [endpoint]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestSequenceRef.current += 1;
      refreshControllerRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setPollingStopped(false);
    void refresh()
      .catch((cause) => {
        if (!cancelled) {
          setError(cause instanceof Error ? cause.message : "读取审批状态失败");
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [refresh]);

  useEffect(() => {
    if (approval?.executionStatus !== "pending" || pollingStopped) {
      return;
    }
    let cancelled = false;
    let timer: number | undefined;
    let attempt = 0;
    const schedule = () => {
      if (cancelled) {
        return;
      }
      if (attempt >= TICKET_POLL_DELAYS_MS.length) {
        if (mountedRef.current) {
          setPollingStopped(true);
        }
        return;
      }
      const delay = TICKET_POLL_DELAYS_MS[attempt];
      attempt += 1;
      timer = window.setTimeout(() => {
        void refresh()
          .then((current) => {
            if (!cancelled && (!current || current.executionStatus === "pending")) {
              schedule();
            }
          })
          .catch((cause) => {
            if (!cancelled) {
              setError(cause instanceof Error ? cause.message : "刷新工单状态失败");
              schedule();
            }
          });
      }, delay);
    };
    schedule();
    return () => {
      cancelled = true;
      if (timer !== undefined) {
        window.clearTimeout(timer);
      }
    };
  }, [approval?.executionStatus, pollingStopped, refresh]);

  const retryRefresh = useCallback(() => {
    setLoading(true);
    setPollingStopped(false);
    void refresh()
      .catch((cause) => {
        setError(cause instanceof Error ? cause.message : "读取审批状态失败");
      })
      .finally(() => {
        if (mountedRef.current) {
          setLoading(false);
        }
      });
  }, [refresh]);

  const act = useCallback(
    async (action: ApprovalAction) => {
      if (submitting || approval?.status !== "pending") {
        return;
      }
      setSubmitting(action);
      setError(null);
      try {
        const response = await fetch(`${endpoint}/${action}`, { method: "POST" });
        if (response.status === 410) {
          setApproval({
            approvalId: prompt.approvalId,
            status: "expired",
            draft: prompt.draft,
            executionStatus: "expired",
          });
          return;
        }
        if (response.status === 409) {
          await refresh();
          return;
        }
        if (!response.ok) {
          throw await readApprovalError(response);
        }
        const current = (await response.json()) as TicketApproval;
        setApproval(current);
        setPollingStopped(false);
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "审批操作失败");
      } finally {
        setSubmitting(null);
      }
    }, [approval?.status, endpoint, prompt.approvalId, prompt.draft, refresh, submitting],
  );

  const draft = approval?.draft ?? prompt.draft;
  const actionable =
    !loading &&
    !submitting &&
    approval?.status === "pending" &&
    approval.executionStatus === "awaiting_confirmation";
  const title =
    approval?.executionStatus === "succeeded"
      ? "工单已创建"
      : approval?.status === "cancelled"
        ? "工单草稿已取消"
        : approval?.status === "expired"
          ? "工单确认已过期"
          : approval?.executionStatus === "pending"
            ? "工单正在创建"
            : "工单草稿待确认";

  return (
    <section className="mt-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
      <p className="flex items-center gap-2 text-sm font-semibold">
        <TicketCheck className="size-4 text-amber-600" aria-hidden />
        {title}
      </p>
      <dl className="mt-3 grid gap-2 text-sm">
        <div>
          <dt className="text-xs text-muted-foreground">标题</dt>
          <dd className="mt-0.5 font-medium">{draft.title}</dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">描述</dt>
          <dd className="mt-0.5 whitespace-pre-wrap">{draft.description}</dd>
        </div>
        <div className="flex items-baseline gap-2">
          <dt className="text-xs text-muted-foreground">优先级</dt>
          <dd className="rounded-md border border-border px-1.5 py-0.5 text-xs">
            {PRIORITY_LABEL[draft.priority]}
          </dd>
        </div>
      </dl>

      {approval?.executionStatus === "succeeded" && approval.ticket ? (
        <p className="mt-3 flex items-center gap-2 text-sm text-emerald-700">
          <CheckCircle2 className="size-4" aria-hidden />
          工单已创建：<span className="font-mono font-semibold">{approval.ticket.number}</span>
        </p>
      ) : approval?.status === "cancelled" ? (
        <p className="mt-3 text-sm text-muted-foreground">已取消，未创建工单。</p>
      ) : approval?.status === "expired" ? (
        <p className="mt-3 flex items-start gap-2 text-sm text-destructive">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
          确认已过期。请重新发送“帮我建个工单”生成新草稿。
        </p>
      ) : approval?.executionStatus === "pending" ? (
        pollingStopped ? (
          <div className="mt-3 text-sm text-muted-foreground">
            <p>工单仍在创建，自动刷新已暂停。</p>
            {!error ? (
              <Button
                className="mt-2"
                variant="outline"
                size="xs"
                disabled={loading}
                onClick={retryRefresh}
              >
                刷新状态
              </Button>
            ) : null}
          </div>
        ) : (
          <p className="mt-3 flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" aria-hidden />
            已确认，正在创建工单…
          </p>
        )
      ) : null}

      {error ? (
        <div className="mt-3 rounded-md border border-destructive/30 bg-destructive/5 p-2 text-xs text-destructive">
          <p>{error}</p>
          <Button
            className="mt-2"
            variant="outline"
            size="xs"
            disabled={loading || Boolean(submitting)}
            onClick={retryRefresh}
          >
            重试
          </Button>
        </div>
      ) : null}

      {approval?.status === "pending" || loading ? (
        <div className="mt-3 flex gap-2">
          <Button
            size="sm"
            disabled={!actionable}
            onClick={() => void act("confirm")}
          >
            {submitting === "confirm" ? "正在确认…" : "确认创建"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!actionable}
            onClick={() => void act("cancel")}
          >
            {submitting === "cancel" ? "正在取消…" : "取消"}
          </Button>
        </div>
      ) : null}
    </section>
  );
}
