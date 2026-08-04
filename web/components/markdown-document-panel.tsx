"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, FileText, Loader2, Plus, RefreshCw, RotateCcw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import type {
  ApiErrorBody,
  FAQIndexStatus,
  KnowledgeBase,
  MarkdownDocument,
  MarkdownDocumentListResponse,
} from "@/lib/types";

const maxMarkdownBytes = 512 << 10;
const markdownContent = z
  .string()
  .trim()
  .min(1, "请输入 Markdown 内容")
  .refine((value) => new TextEncoder().encode(value).byteLength <= maxMarkdownBytes, {
    message: "Markdown 内容不能超过 512 KiB",
  });
const createSchema = z.object({
  title: z.string().trim().min(1, "请输入文档标题").max(500, "标题不能超过 500 个字符"),
  sourceUrl: z
    .string()
    .trim()
    .refine((value) => value === "" || /^https?:\/\//i.test(value), "来源地址必须是 HTTP(S) URL"),
  content: markdownContent,
});
const versionSchema = z.object({ content: markdownContent });
type CreateForm = z.infer<typeof createSchema>;
type VersionForm = z.infer<typeof versionSchema>;

const statusLabel: Record<FAQIndexStatus, string> = {
  pending: "等待索引",
  indexing: "索引中",
  ready: "已就绪",
  failed: "失败",
};

async function apiError(response: Response, fallback: string): Promise<Error> {
  try {
    const body = (await response.json()) as ApiErrorBody;
    return new Error(body.error?.message || body.error?.code || fallback);
  } catch {
    return new Error(fallback);
  }
}

function Status({ status }: { status: FAQIndexStatus }) {
  const tone =
    status === "ready"
      ? "border-emerald-200 bg-emerald-50 text-emerald-700"
      : status === "failed"
        ? "border-destructive/30 bg-destructive/5 text-destructive"
        : "border-amber-200 bg-amber-50 text-amber-700";
  return (
    <span className={`rounded-full border px-2 py-0.5 text-xs ${tone}`}>
      {statusLabel[status]}
    </span>
  );
}

export function MarkdownDocumentPanel({ base }: { base?: KnowledgeBase }) {
  const [documents, setDocuments] = useState<MarkdownDocument[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [versionTarget, setVersionTarget] = useState<MarkdownDocument | null>(null);
  const [pollingStopped, setPollingStopped] = useState(false);
  const requestSequence = useRef(0);
  const controllerRef = useRef<AbortController | null>(null);
  const createForm = useForm<CreateForm>({
    resolver: zodResolver(createSchema),
    defaultValues: { title: "", sourceUrl: "", content: "" },
  });
  const versionForm = useForm<VersionForm>({
    resolver: zodResolver(versionSchema),
    defaultValues: { content: "" },
  });
  const resetCreateForm = createForm.reset;
  const resetVersionForm = versionForm.reset;

  const loadDocuments = useCallback(async (showLoading = true) => {
    if (!base) {
      setDocuments([]);
      return null;
    }
    const sequence = ++requestSequence.current;
    controllerRef.current?.abort();
    const controller = new AbortController();
    controllerRef.current = controller;
    if (showLoading) setLoading(true);
    try {
      const response = await fetch(
        `/api/admin/knowledge-bases/${encodeURIComponent(base.id)}/documents`,
        { cache: "no-store", signal: controller.signal },
      );
      if (!response.ok) throw await apiError(response, "读取 Markdown 文档失败");
      const body = (await response.json()) as MarkdownDocumentListResponse;
      if (
        !Array.isArray(body.items) ||
        !body.items.every(
          (item) =>
            typeof item?.id === "string" &&
            typeof item?.title === "string" &&
            Array.isArray(item?.versions),
        )
      ) {
        throw new Error("Markdown 文档响应格式无效");
      }
      if (controller.signal.aborted || sequence !== requestSequence.current) return null;
      setDocuments(body.items);
      setError(null);
      return body.items;
    } catch (cause) {
      if (controller.signal.aborted || sequence !== requestSequence.current) return null;
      setError(cause instanceof Error ? cause.message : "读取 Markdown 文档失败");
      return null;
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }, [base]);

  useEffect(() => {
    setVersionTarget(null);
    setPollingStopped(false);
    resetCreateForm();
    resetVersionForm();
    void loadDocuments();
    return () => {
      requestSequence.current += 1;
      controllerRef.current?.abort();
    };
  }, [base?.id, loadDocuments, resetCreateForm, resetVersionForm]);

  const hasProcessing = documents.some((document) =>
    document.versions.some((version) => version.status === "pending" || version.status === "indexing"),
  );
  useEffect(() => {
    if (!base || !hasProcessing || pollingStopped) return;
    let cancelled = false;
    let attempts = 0;
    const timer = window.setInterval(() => {
      attempts += 1;
      void loadDocuments(false);
      if (attempts >= 10 && !cancelled) {
        window.clearInterval(timer);
        setPollingStopped(true);
      }
    }, 3_000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [base, hasProcessing, loadDocuments, pollingStopped]);

  const createDocument = createForm.handleSubmit(async (values) => {
    if (!base || base.status !== "active") return;
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch(
        `/api/admin/knowledge-bases/${encodeURIComponent(base.id)}/documents`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(values) },
      );
      if (!response.ok) throw await apiError(response, "创建 Markdown 文档失败");
      createForm.reset();
      setPollingStopped(false);
      await loadDocuments(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "创建 Markdown 文档失败");
    } finally {
      setSubmitting(false);
    }
  });

  const beginVersion = async (document: MarkdownDocument) => {
    if (!base) return;
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch(
        `/api/admin/knowledge-bases/${encodeURIComponent(base.id)}/documents/${encodeURIComponent(document.id)}`,
        { cache: "no-store" },
      );
      if (!response.ok) throw await apiError(response, "读取 Markdown 源内容失败");
      const detail = (await response.json()) as MarkdownDocument;
      setVersionTarget(detail);
      versionForm.reset({ content: detail.latestContent ?? "" });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "读取 Markdown 源内容失败");
    } finally {
      setSubmitting(false);
    }
  };

  const createVersion = versionForm.handleSubmit(async (values) => {
    if (!base || !versionTarget) return;
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch(
        `/api/admin/knowledge-bases/${encodeURIComponent(base.id)}/documents/${encodeURIComponent(versionTarget.id)}/versions`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(values) },
      );
      if (!response.ok) throw await apiError(response, "创建 Markdown 新版本失败");
      setVersionTarget(null);
      versionForm.reset();
      setPollingStopped(false);
      await loadDocuments(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "创建 Markdown 新版本失败");
    } finally {
      setSubmitting(false);
    }
  });

  const retryVersion = async (documentId: string, versionId: string) => {
    if (!base) return;
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch(
        `/api/admin/knowledge-bases/${encodeURIComponent(base.id)}/documents/${encodeURIComponent(documentId)}/versions/${encodeURIComponent(versionId)}/retry`,
        { method: "POST" },
      );
      if (!response.ok) throw await apiError(response, "重试索引失败");
      setPollingStopped(false);
      await loadDocuments(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "重试索引失败");
    } finally {
      setSubmitting(false);
    }
  };

  const disabled = !base || base.status !== "active" || submitting;
  return (
    <section className="space-y-4 border-t border-border pt-6">
      <header className="flex flex-wrap items-start gap-3">
        <div>
          <h2 className="text-xl font-semibold">Markdown 文档</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            保存后创建持久化索引任务；新版本发布前，旧活动版本继续提供检索。
          </p>
        </div>
        <Button className="ml-auto" size="sm" variant="outline" onClick={() => void loadDocuments()} disabled={!base || loading}>
          <RefreshCw className={loading ? "animate-spin" : ""} aria-hidden /> 刷新
        </Button>
      </header>

      {error ? (
        <div className="flex items-start gap-2 rounded-xl border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">
          <AlertTriangle className="mt-0.5 size-4" aria-hidden /><span>{error}</span>
        </div>
      ) : null}

      <form onSubmit={createDocument} className="grid gap-3 rounded-xl border border-border bg-card p-4 sm:p-5">
        <div className="flex items-center gap-2"><FileText className="size-4" aria-hidden /><h3 className="font-medium">新建 Markdown 文档</h3></div>
        <input {...createForm.register("title")} placeholder="文档标题" disabled={disabled} className="h-9 rounded-lg border border-input bg-background px-3 text-sm" />
        {createForm.formState.errors.title ? <p className="text-xs text-destructive">{createForm.formState.errors.title.message}</p> : null}
        <input {...createForm.register("sourceUrl")} placeholder="来源 URL（可选）" disabled={disabled} className="h-9 rounded-lg border border-input bg-background px-3 text-sm" />
        {createForm.formState.errors.sourceUrl ? <p className="text-xs text-destructive">{createForm.formState.errors.sourceUrl.message}</p> : null}
        <textarea aria-label="Markdown 内容" {...createForm.register("content")} rows={9} placeholder="# 标题&#10;&#10;Markdown 正文" disabled={disabled} className="rounded-lg border border-input bg-background p-3 font-mono text-sm" />
        {createForm.formState.errors.content ? <p className="text-xs text-destructive">{createForm.formState.errors.content.message}</p> : null}
        <Button className="w-fit" type="submit" disabled={disabled}>
          {submitting ? <Loader2 className="animate-spin" aria-hidden /> : <Plus aria-hidden />} 保存并开始索引
        </Button>
      </form>

      {loading && documents.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" aria-hidden />正在读取 Markdown 文档…</p>
      ) : documents.length === 0 ? (
        <div className="rounded-xl border border-dashed border-border p-6 text-center text-sm text-muted-foreground">当前知识库还没有 Markdown 文档。</div>
      ) : (
        <div className="space-y-3">
          {documents.map((document) => (
            <article key={document.id} className="rounded-xl border border-border bg-card p-4 sm:p-5">
              <div className="flex flex-wrap items-start gap-3">
                <div><h3 className="font-semibold">{document.title}</h3><p className="mt-1 font-mono text-xs text-muted-foreground">{document.id} · 最新 v{document.latestVersion}</p></div>
                <Button className="ml-auto" size="sm" variant="outline" disabled={disabled} onClick={() => void beginVersion(document)}>创建新版本</Button>
              </div>
              {document.sourceUrl ? <a href={document.sourceUrl} target="_blank" rel="noreferrer" className="mt-2 block text-xs text-primary underline underline-offset-4">{document.sourceUrl}</a> : null}
              <div className="mt-3 space-y-2">
                {document.versions.map((version) => (
                  <div key={version.id} className="flex flex-wrap items-center gap-2 rounded-lg bg-muted/45 px-3 py-2 text-xs">
                    <span className="font-medium">v{version.number}</span><Status status={version.status} />
                    {version.active ? <span className="text-emerald-700">当前活动版本</span> : null}
                    <span className="font-mono text-muted-foreground">{version.id}</span>
                    {version.errorCode ? <span className="text-destructive">{version.errorCode}</span> : null}
                    {version.status === "failed" ? (
                      <Button className="ml-auto" size="sm" variant="outline" disabled={submitting} onClick={() => void retryVersion(document.id, version.id)}><RotateCcw aria-hidden />重试</Button>
                    ) : null}
                  </div>
                ))}
              </div>
            </article>
          ))}
        </div>
      )}

      {pollingStopped && hasProcessing ? <p className="text-xs text-amber-700">自动刷新已暂停；索引任务仍保留，可点击刷新继续查看。</p> : null}

      {versionTarget ? (
        <form onSubmit={createVersion} className="rounded-xl border border-primary/30 bg-card p-4 sm:p-5">
          <h3 className="font-medium">为“{versionTarget.title}”创建新版本</h3>
          <p className="mt-1 text-xs text-muted-foreground">已载入最新源内容；提交后旧活动版本继续服务，直到新版本索引成功。</p>
          <textarea aria-label="新版本 Markdown 内容" {...versionForm.register("content")} rows={12} disabled={submitting} className="mt-3 w-full rounded-lg border border-input bg-background p-3 font-mono text-sm" />
          {versionForm.formState.errors.content ? <p className="mt-1 text-xs text-destructive">{versionForm.formState.errors.content.message}</p> : null}
          <div className="mt-3 flex gap-2"><Button type="submit" disabled={submitting}>{submitting ? <Loader2 className="animate-spin" aria-hidden /> : <Plus aria-hidden />}提交新版本</Button><Button type="button" variant="outline" onClick={() => setVersionTarget(null)} disabled={submitting}>取消</Button></div>
        </form>
      ) : null}
    </section>
  );
}
