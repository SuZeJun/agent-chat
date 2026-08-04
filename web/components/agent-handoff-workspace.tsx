"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, Loader2, RefreshCw, UserRoundCheck } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import type { ApiErrorBody, HandoffConversation, HandoffQueueResponse } from "@/lib/types";

const messageSchema = z.object({
  content: z.string().trim().min(1, "请输入回复内容").max(16_000, "回复不能超过 16000 个字符"),
});
type MessageForm = z.infer<typeof messageSchema>;

async function responseError(response: Response, fallback: string): Promise<Error> {
  try {
    const body = (await response.json()) as ApiErrorBody;
    return new Error(body.error?.message || body.error?.code || fallback);
  } catch {
    return new Error(fallback);
  }
}

export function AgentHandoffWorkspace() {
  const [queue, setQueue] = useState<HandoffConversation[]>([]);
  const [selected, setSelected] = useState<HandoffConversation | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const form = useForm<MessageForm>({
    resolver: zodResolver(messageSchema),
    defaultValues: { content: "" },
  });

  const loadQueue = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true);
    try {
      const response = await fetch("/api/agent/conversations", { cache: "no-store" });
      if (!response.ok) throw await responseError(response, "读取等待队列失败");
      const body = (await response.json()) as HandoffQueueResponse;
      if (!Array.isArray(body.items)) throw new Error("等待队列响应格式无效");
      setQueue(body.items);
      setError(null);
      return body.items;
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "读取等待队列失败");
      return null;
    } finally {
      if (showLoading) setLoading(false);
    }
  }, []);

  const loadConversation = useCallback(async (conversationId: string) => {
    try {
      const response = await fetch(`/api/agent/conversations/${encodeURIComponent(conversationId)}`, { cache: "no-store" });
      if (response.status === 404) {
        setSelected(null);
        return null;
      }
      if (!response.ok) throw await responseError(response, "读取接管会话失败");
      const detail = (await response.json()) as HandoffConversation;
      setSelected(detail);
      setError(null);
      return detail;
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "读取接管会话失败");
      return null;
    }
  }, []);

  useEffect(() => {
    void loadQueue(true);
    const timer = window.setInterval(() => {
      void loadQueue();
      if (selected?.id) void loadConversation(selected.id);
    }, 2_000);
    return () => window.clearInterval(timer);
  }, [loadConversation, loadQueue, selected?.id]);

  const runAction = async (path: string, init?: RequestInit) => {
    if (!selected) return;
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch(`/api/agent/conversations/${encodeURIComponent(selected.id)}${path}`, {
        method: "POST",
        ...init,
      });
      if (!response.ok) throw await responseError(response, "人工接管操作失败");
      const detail = (await response.json()) as HandoffConversation;
      // 恢复 AI 后会话已离开人工队列，清空旧详情避免继续显示不可执行操作。
      setSelected(path === "/resume-ai" && detail.status === "ai_active" ? null : detail);
      await loadQueue();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "人工接管操作失败");
    } finally {
      setSubmitting(false);
    }
  };

  const send = form.handleSubmit(async ({ content }) => {
    if (!selected) return;
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch(`/api/agent/conversations/${encodeURIComponent(selected.id)}/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ content }),
      });
      if (!response.ok) throw await responseError(response, "发送人工回复失败");
      form.reset();
      await loadConversation(selected.id);
      await loadQueue();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "发送人工回复失败");
    } finally {
      setSubmitting(false);
    }
  });

  return (
    <main className="min-h-dvh bg-muted/25 p-4 sm:p-6">
      <header className="mx-auto mb-5 flex max-w-7xl flex-wrap items-center gap-3">
        <div>
          <p className="text-xs font-medium tracking-[0.18em] text-muted-foreground uppercase">Human support</p>
          <h1 className="text-2xl font-semibold">客服接管工作台</h1>
        </div>
        <Button className="ml-auto" variant="outline" size="sm" onClick={() => void loadQueue(true)} disabled={loading}>
          <RefreshCw className={loading ? "animate-spin" : ""} aria-hidden />刷新
        </Button>
      </header>

      {error ? (
        <div className="mx-auto mb-4 flex max-w-7xl gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
          <AlertTriangle className="size-4" aria-hidden />{error}
        </div>
      ) : null}

      <div className="mx-auto grid max-w-7xl gap-4 lg:grid-cols-[20rem_minmax(0,1fr)]">
        <aside className="rounded-xl border bg-card p-3">
          <h2 className="px-2 py-1 text-sm font-semibold">等待与我的会话</h2>
          {loading ? (
            <p className="flex items-center gap-2 p-3 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" />读取中…</p>
          ) : queue.length === 0 ? (
            <p className="p-3 text-sm text-muted-foreground">当前没有等待人工的会话。</p>
          ) : (
            <div className="mt-2 space-y-2">
              {queue.map((item) => (
                <button key={item.id} type="button" onClick={() => void loadConversation(item.id)} className={`w-full rounded-lg border p-3 text-left text-sm ${selected?.id === item.id ? "border-primary bg-primary/5" : "border-border"}`}>
                  <div className="flex items-center gap-2"><span className="font-medium">{item.customerId}</span><span className="ml-auto text-xs text-muted-foreground">{item.status === "waiting_human" ? "等待中" : "处理中"}</span></div>
                  <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">{item.summary.customerRequest}</p>
                </button>
              ))}
            </div>
          )}
        </aside>

        {!selected ? (
          <section className="rounded-xl border border-dashed bg-card p-10 text-center text-sm text-muted-foreground">选择一条会话查看摘要和历史。</section>
        ) : (
          <section className="space-y-4">
            <div className="rounded-xl border bg-card p-4 sm:p-5">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="font-semibold">接管摘要</h2>
                <span className="rounded-full border px-2 py-0.5 text-xs">{selected.status}</span>
                {selected.status === "waiting_human" ? (
                  <Button className="ml-auto" size="sm" onClick={() => void runAction("/takeover")} disabled={submitting}><UserRoundCheck aria-hidden />接管</Button>
                ) : (
                  <Button className="ml-auto" size="sm" variant="outline" onClick={() => void runAction("/resume-ai")} disabled={submitting}>恢复 AI</Button>
                )}
              </div>
              <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
                <div><dt className="text-xs text-muted-foreground">客户诉求</dt><dd className="mt-1 whitespace-pre-wrap">{selected.summary.customerRequest}</dd></div>
                <div><dt className="text-xs text-muted-foreground">建议动作</dt><dd className="mt-1 whitespace-pre-wrap">{selected.summary.recommendedAction}</dd></div>
                <div><dt className="text-xs text-muted-foreground">未解决问题</dt><dd className="mt-1">{selected.summary.unresolvedQuestions.join("；") || "无"}</dd></div>
                <div><dt className="text-xs text-muted-foreground">风险信号</dt><dd className="mt-1">{selected.summary.riskSignals.join("；") || "未识别"}</dd></div>
              </dl>
              <div className="mt-4 grid gap-3 sm:grid-cols-2">
                <div><h3 className="text-xs font-medium text-muted-foreground">引用</h3>{selected.summary.citations.length ? selected.summary.citations.map((citation) => <p key={`${citation.sourceId}-${citation.versionId}`} className="mt-1 rounded bg-muted p-2 text-xs"><strong>{citation.title}</strong>：{citation.excerpt}</p>) : <p className="mt-1 text-xs text-muted-foreground">无引用</p>}</div>
                <div><h3 className="text-xs font-medium text-muted-foreground">工具记录</h3>{selected.summary.toolCalls.length ? selected.summary.toolCalls.map((tool, index) => <p key={`${tool.name}-${index}`} className="mt-1 rounded bg-muted p-2 font-mono text-xs">{tool.name} · {tool.status}{tool.errorCode ? ` · ${tool.errorCode}` : ""}</p>) : <p className="mt-1 text-xs text-muted-foreground">无工具调用</p>}</div>
              </div>
            </div>

            <div className="rounded-xl border bg-card p-4 sm:p-5">
              <h2 className="font-semibold">会话历史</h2>
              <div className="mt-3 max-h-[26rem] space-y-2 overflow-y-auto">
                {(selected.messages ?? []).map((message) => (
                  <div key={message.id} className={`rounded-lg p-2 text-sm ${message.role === "customer" ? "ml-10 bg-primary text-primary-foreground" : message.role === "agent" ? "mr-10 bg-sky-50 text-sky-950" : "mr-10 bg-muted"}`}>
                    <p className="mb-1 text-[11px] opacity-70">{message.role}</p><p className="whitespace-pre-wrap">{message.content}</p>
                  </div>
                ))}
              </div>
              {selected.status === "human_active" ? (
                <form onSubmit={send} className="mt-4 flex gap-2">
                  <input {...form.register("content")} className="h-9 flex-1 rounded-lg border bg-background px-3 text-sm" placeholder="输入人工回复" disabled={submitting} />
                  <Button type="submit" disabled={submitting}>发送</Button>
                </form>
              ) : null}
              {form.formState.errors.content ? <p className="mt-1 text-xs text-destructive">{form.formState.errors.content.message}</p> : null}
            </div>
          </section>
        )}
      </div>
    </main>
  );
}
