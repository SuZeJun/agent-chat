"use client";

import {
  createColumnHelper,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, CheckCircle2, FileUp, Loader2, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { Button } from "@/components/ui/button";
import { MarkdownDocumentPanel } from "@/components/markdown-document-panel";
import type {
  ApiErrorBody,
  FAQImportItem,
  FAQImportResult,
  FAQImportStatus,
  FAQIndexStatus,
  KnowledgeBase,
  KnowledgeBaseListResponse,
} from "@/lib/types";

const maxFAQUploadBytes = 2 << 20;
const pollDelaysMs = [1_000, 1_500, 2_500, 4_000, 6_000, 8_000, 10_000, 10_000];
const emptyItems: FAQImportItem[] = [];
const tableFeatureSet = tableFeatures({});
const columnHelper = createColumnHelper<typeof tableFeatureSet, FAQImportItem>();

const uploadSchema = z.object({
  file: z.custom<File>(
    (value) =>
      typeof File !== "undefined" &&
      value instanceof File &&
      value.size > 0 &&
      value.size <= maxFAQUploadBytes &&
      value.name.toLowerCase().endsWith(".csv"),
    "请选择不超过 2 MiB 的非空 CSV 文件",
  ),
});

type UploadForm = z.infer<typeof uploadSchema>;

const statusLabel: Record<FAQIndexStatus, string> = {
  pending: "等待索引",
  indexing: "索引中",
  ready: "已就绪",
  failed: "失败",
};

function StatusBadge({ status }: { status: FAQIndexStatus }) {
  const tone =
    status === "ready"
      ? "border-emerald-200 bg-emerald-50 text-emerald-700"
      : status === "failed"
        ? "border-destructive/30 bg-destructive/5 text-destructive"
        : "border-amber-200 bg-amber-50 text-amber-700";
  return (
    <span className={`inline-flex rounded-full border px-2 py-0.5 text-xs ${tone}`}>
      {statusLabel[status]}
    </span>
  );
}

const columns = columnHelper.columns([
  columnHelper.accessor("rowNumber", { header: "CSV 行", cell: (info) => info.getValue() }),
  columnHelper.accessor("status", {
    header: "索引状态",
    cell: (info) => <StatusBadge status={info.getValue()} />,
  }),
  columnHelper.accessor("documentId", { header: "文档 ID", cell: (info) => info.getValue() }),
  columnHelper.accessor("errorCode", {
    header: "失败原因",
    cell: (info) => info.getValue() || "—",
  }),
]);

async function readAPIError(response: Response, fallback: string): Promise<Error> {
  try {
    const body = (await response.json()) as ApiErrorBody;
    return new Error(body.error?.message || body.error?.code || fallback);
  } catch {
    return new Error(fallback);
  }
}

export function FAQAdminPanel() {
  const [bases, setBases] = useState<KnowledgeBase[]>([]);
  const [selectedBaseId, setSelectedBaseId] = useState("");
  const [basesLoading, setBasesLoading] = useState(true);
  const [basesError, setBasesError] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [importTarget, setImportTarget] = useState<{ baseId: string; importId: string } | null>(null);
  const [importStatus, setImportStatus] = useState<FAQImportStatus | null>(null);
  const [importError, setImportError] = useState<string | null>(null);
  const [pollingStopped, setPollingStopped] = useState(false);
  const [pollCycle, setPollCycle] = useState(0);
  const mountedRef = useRef(true);
  const statusSequenceRef = useRef(0);
  const statusControllerRef = useRef<AbortController | null>(null);
  const uploadControllerRef = useRef<AbortController | null>(null);
  const { control, handleSubmit, reset, formState } = useForm<UploadForm>({
    resolver: zodResolver(uploadSchema),
  });

  const loadBases = useCallback(async () => {
    setBasesLoading(true);
    setBasesError(null);
    try {
      const response = await fetch("/api/admin/knowledge-bases", { cache: "no-store" });
      if (!response.ok) {
        throw await readAPIError(response, "读取知识库失败");
      }
      const body = (await response.json()) as KnowledgeBaseListResponse;
      if (!Array.isArray(body.items)) {
        throw new Error("知识库响应格式无效");
      }
      if (!mountedRef.current) {
        return;
      }
      setBases(body.items);
      setSelectedBaseId((current) => {
        if (body.items.some((base) => base.id === current)) {
          return current;
        }
        return body.items.find((base) => base.status === "active")?.id ?? body.items[0]?.id ?? "";
      });
    } catch (cause) {
      if (mountedRef.current) {
        setBasesError(cause instanceof Error ? cause.message : "读取知识库失败");
      }
    } finally {
      if (mountedRef.current) {
        setBasesLoading(false);
      }
    }
  }, []);

  const refreshImport = useCallback(async (baseId: string, importId: string) => {
    const sequence = ++statusSequenceRef.current;
    statusControllerRef.current?.abort();
    const controller = new AbortController();
    statusControllerRef.current = controller;
    try {
      const response = await fetch(
        `/api/admin/knowledge-bases/${encodeURIComponent(baseId)}/faq-imports/${encodeURIComponent(importId)}`,
        { cache: "no-store", signal: controller.signal },
      );
      if (!response.ok) {
        throw await readAPIError(response, "刷新导入状态失败");
      }
      const current = (await response.json()) as FAQImportStatus;
      if (
        !mountedRef.current ||
        controller.signal.aborted ||
        sequence !== statusSequenceRef.current
      ) {
        return null;
      }
      setImportStatus(current);
      setImportError(null);
      return current;
    } catch (cause) {
      if (controller.signal.aborted || sequence !== statusSequenceRef.current) {
        return null;
      }
      throw cause;
    } finally {
      if (statusControllerRef.current === controller) {
        statusControllerRef.current = null;
      }
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void loadBases();
    return () => {
      mountedRef.current = false;
      statusSequenceRef.current += 1;
      statusControllerRef.current?.abort();
      uploadControllerRef.current?.abort();
    };
  }, [loadBases]);

  const currentImportStatus = importStatus?.status;

  useEffect(() => {
    if (
      !importTarget ||
      !currentImportStatus ||
      pollingStopped ||
      (currentImportStatus !== "pending" && currentImportStatus !== "indexing")
    ) {
      return;
    }
    let cancelled = false;
    let timer: number | undefined;
    let attempt = 0;
    const schedule = () => {
      if (cancelled) {
        return;
      }
      if (attempt >= pollDelaysMs.length) {
        setPollingStopped(true);
        return;
      }
      const delay = pollDelaysMs[attempt];
      attempt += 1;
      timer = window.setTimeout(() => {
        void refreshImport(importTarget.baseId, importTarget.importId)
          .then((current) => {
            if (
              !cancelled &&
              (!current || current.status === "pending" || current.status === "indexing")
            ) {
              schedule();
            }
          })
          .catch((cause) => {
            if (!cancelled) {
              setImportError(cause instanceof Error ? cause.message : "刷新导入状态失败");
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
  }, [currentImportStatus, importTarget, pollCycle, pollingStopped, refreshImport]);

  const selectedBase = bases.find((base) => base.id === selectedBaseId);
  const table = useTable({
    features: tableFeatureSet,
    columns,
    data: importStatus?.items ?? emptyItems,
  });

  const changeBase = (baseId: string) => {
    statusSequenceRef.current += 1;
    statusControllerRef.current?.abort();
    setSelectedBaseId(baseId);
    setImportTarget(null);
    setImportStatus(null);
    setImportError(null);
    setPollingStopped(false);
    reset();
  };

  const submitUpload = handleSubmit(async ({ file }) => {
    if (!selectedBase || selectedBase.status !== "active") {
      setImportError("请选择可用的知识库");
      return;
    }
    setUploading(true);
    setImportError(null);
    setImportTarget(null);
    setImportStatus(null);
    setPollingStopped(false);
    statusSequenceRef.current += 1;
    statusControllerRef.current?.abort();
    uploadControllerRef.current?.abort();
    const uploadController = new AbortController();
    uploadControllerRef.current = uploadController;
    try {
      const form = new FormData();
      form.append("file", file, file.name);
      const response = await fetch(
        `/api/admin/knowledge-bases/${encodeURIComponent(selectedBase.id)}/faq-imports`,
        { method: "POST", body: form, signal: uploadController.signal },
      );
      if (!response.ok) {
        throw await readAPIError(response, "FAQ 导入失败");
      }
      const result = (await response.json()) as FAQImportResult;
      if (!mountedRef.current || uploadController.signal.aborted) {
        return;
      }
      const target = { baseId: selectedBase.id, importId: result.id };
      setImportTarget(target);
      const current = await refreshImport(target.baseId, target.importId);
      if (current?.status === "pending" || current?.status === "indexing") {
        setPollCycle((value) => value + 1);
      }
      reset();
    } catch (cause) {
      if (uploadController.signal.aborted) {
        return;
      }
      if (mountedRef.current) {
        setImportError(cause instanceof Error ? cause.message : "FAQ 导入失败");
      }
    } finally {
      if (uploadControllerRef.current === uploadController) {
        uploadControllerRef.current = null;
      }
      if (mountedRef.current && !uploadController.signal.aborted) {
        setUploading(false);
      }
    }
  });

  const retryImport = () => {
    if (!importTarget) {
      return;
    }
    setImportError(null);
    setPollingStopped(false);
    setPollCycle((value) => value + 1);
    void refreshImport(importTarget.baseId, importTarget.importId).catch((cause) => {
      setImportError(cause instanceof Error ? cause.message : "刷新导入状态失败");
    });
  };

  return (
    <div className="mx-auto max-w-6xl space-y-5 p-4 sm:p-6">
      <header>
        <p className="text-xs font-medium tracking-[0.18em] text-muted-foreground uppercase">
          Knowledge operations
        </p>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">知识管理与索引状态</h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          选择知识库并上传 UTF-8 CSV。每一行会独立进入持久化索引任务，页面自动跟踪结果。
        </p>
      </header>

      <section className="grid gap-4 rounded-xl border border-border bg-card p-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] sm:p-5">
        <div>
          <label htmlFor="knowledge-base" className="text-sm font-medium">
            目标知识库
          </label>
          {basesLoading ? (
            <p className="mt-2 flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" aria-hidden /> 正在读取知识库…
            </p>
          ) : basesError ? (
            <div className="mt-2 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
              <p>{basesError}</p>
              <Button className="mt-2" size="sm" variant="outline" onClick={() => void loadBases()}>
                重试
              </Button>
            </div>
          ) : bases.length === 0 ? (
            <p className="mt-2 rounded-lg border border-dashed border-border p-3 text-sm text-muted-foreground">
              尚无知识库。请先通过管理员 API 创建知识库。
            </p>
          ) : (
            <>
              <select
                id="knowledge-base"
                value={selectedBaseId}
                disabled={uploading}
                onChange={(event) => changeBase(event.target.value)}
                className="mt-2 h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
              >
                {bases.map((base) => (
                  <option key={base.id} value={base.id}>
                    {base.name}{base.status === "disabled" ? "（已停用）" : ""}
                  </option>
                ))}
              </select>
              <p className="mt-2 text-xs text-muted-foreground">
                {selectedBase?.description || selectedBase?.id}
              </p>
            </>
          )}
        </div>

        <form onSubmit={submitUpload} className="rounded-lg bg-muted/45 p-4">
          <div className="flex items-start gap-3">
            <span className="rounded-lg border border-border bg-background p-2">
              <FileUp className="size-4" aria-hidden />
            </span>
            <div className="min-w-0 flex-1">
              <label htmlFor="faq-file" className="text-sm font-medium">
                FAQ CSV
              </label>
              <p className="mt-0.5 text-xs text-muted-foreground">
                表头必须为 question,answer，可选第三列 source_url。
              </p>
            </div>
          </div>
          <Controller
            control={control}
            name="file"
            render={({ field: { onChange, ref } }) => (
              <input
                id="faq-file"
                ref={ref}
                type="file"
                accept=".csv,text/csv"
                disabled={uploading || !selectedBase || selectedBase.status !== "active"}
                onChange={(event) => onChange(event.target.files?.[0])}
                className="mt-3 block w-full text-sm file:mr-3 file:rounded-md file:border-0 file:bg-background file:px-3 file:py-2 file:text-sm file:font-medium"
              />
            )}
          />
          {formState.errors.file ? (
            <p className="mt-2 text-xs text-destructive">{formState.errors.file.message}</p>
          ) : null}
          <Button
            className="mt-3"
            type="submit"
            disabled={uploading || !selectedBase || selectedBase.status !== "active"}
          >
            {uploading ? <Loader2 className="animate-spin" aria-hidden /> : <FileUp aria-hidden />}
            {uploading ? "正在提交…" : "导入并开始索引"}
          </Button>
        </form>
      </section>

      {importError ? (
        <section className="rounded-xl border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
            <div>
              <p className="font-medium">处理失败</p>
              <p className="mt-0.5">{importError}</p>
              {importTarget ? (
                <Button className="mt-2" size="sm" variant="outline" onClick={retryImport}>
                  <RefreshCw aria-hidden /> 重试刷新
                </Button>
              ) : null}
            </div>
          </div>
        </section>
      ) : null}

      {importStatus ? (
        <section className="overflow-hidden rounded-xl border border-border bg-card">
          <div className="flex flex-wrap items-start gap-3 border-b border-border p-4 sm:p-5">
            <div>
              <div className="flex items-center gap-2">
                <h2 className="font-semibold">{importStatus.sourceName}</h2>
                <StatusBadge status={importStatus.status} />
              </div>
              <p className="mt-1 font-mono text-xs text-muted-foreground">
                {importStatus.id} · {new Date(importStatus.createdAt).toLocaleString("zh-CN")}
              </p>
            </div>
            {pollingStopped ? (
              <Button className="ml-auto" size="sm" variant="outline" onClick={retryImport}>
                <RefreshCw aria-hidden /> 刷新状态
              </Button>
            ) : null}
          </div>

          <dl className="grid grid-cols-2 gap-px bg-border sm:grid-cols-4">
            {[
              ["总行数", importStatus.totalRows],
              ["已成功", importStatus.readyRows],
              ["处理中", Math.max(0, importStatus.totalRows - importStatus.readyRows - importStatus.failedRows)],
              ["失败", importStatus.failedRows],
            ].map(([label, value]) => (
              <div key={label} className="bg-card p-4">
                <dt className="text-xs text-muted-foreground">{label}</dt>
                <dd className="mt-1 text-xl font-semibold tabular-nums">{value}</dd>
              </div>
            ))}
          </dl>

          {pollingStopped && (importStatus.status === "pending" || importStatus.status === "indexing") ? (
            <p className="border-b border-border bg-amber-50 px-4 py-2 text-xs text-amber-800">
              自动刷新已暂停；索引任务仍保留在队列中，可稍后手动刷新。
            </p>
          ) : null}

          <div className="overflow-x-auto p-4 sm:p-5">
            <table className="w-full min-w-3xl text-left text-sm">
              <thead className="text-xs text-muted-foreground">
                {table.getHeaderGroups().map((group) => (
                  <tr key={group.id} className="border-b border-border">
                    {group.headers.map((header) => (
                      <th key={header.id} className="px-3 py-2 font-medium first:pl-0 last:pr-0">
                        {header.isPlaceholder ? null : <table.FlexRender header={header} />}
                      </th>
                    ))}
                  </tr>
                ))}
              </thead>
              <tbody>
                {table.getRowModel().rows.map((row) => (
                  <tr key={row.id} className="border-b border-border/60 last:border-0">
                    {row.getAllCells().map((cell) => (
                      <td
                        key={cell.id}
                        className="px-3 py-3 font-mono text-xs first:pl-0 last:pr-0"
                      >
                        <table.FlexRender cell={cell} />
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {importStatus.status === "ready" ? (
            <p className="flex items-center gap-2 border-t border-border bg-emerald-50 px-4 py-3 text-sm text-emerald-700">
              <CheckCircle2 className="size-4" aria-hidden /> 全部 FAQ 已完成索引并发布。
            </p>
          ) : null}
        </section>
      ) : !uploading && !importError ? (
        <section className="rounded-xl border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          导入 CSV 后，这里会展示总数、成功数、失败数和每行索引状态。
        </section>
      ) : null}

      <MarkdownDocumentPanel base={selectedBase} />
    </div>
  );
}
