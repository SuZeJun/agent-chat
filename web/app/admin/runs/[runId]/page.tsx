import Link from "next/link";
import { notFound } from "next/navigation";

import { RunTraceView } from "@/components/run-trace-view";
import { readServerConfig } from "@/lib/config";
import type { RunTrace } from "@/lib/types";

export const dynamic = "force-dynamic";

type PageProps = { params: Promise<{ runId: string }> };

/**
 * 管理员运行详情。
 *
 * 直接在服务端取 Trace 而不经由自身的 BFF 路由：页面本身就是服务端组件，
 * 多绕一跳只会增加一次网络往返。BFF 路由保留给客户端按需刷新使用。
 */
async function loadTrace(runId: string): Promise<RunTrace | null> {
  const config = readServerConfig();
  const response = await fetch(
    `${config.apiBaseUrl}/api/v1/admin/agent-runs/${encodeURIComponent(runId)}`,
    { headers: { "X-Admin-ID": config.adminId }, cache: "no-store" },
  );
  if (!response.ok) {
    return null;
  }
  return (await response.json()) as RunTrace;
}

export default async function RunDetailPage({ params }: PageProps) {
  const { runId } = await params;
  const trace = await loadTrace(runId);
  if (!trace) {
    notFound();
  }

  return (
    <main className="min-h-dvh">
      <div className="border-b border-border bg-muted/40 px-4 py-2">
        <p className="mx-auto flex max-w-4xl items-center gap-2 text-xs text-muted-foreground">
          <span className="rounded-md border border-border px-1.5 py-0.5">内部视图</span>
          面向管理员与 AI 运营人员，包含检索分数与模型用量，不对客户展示。
          <Link href="/" className="ml-auto underline underline-offset-4">
            返回聊天
          </Link>
        </p>
      </div>
      <RunTraceView trace={trace} />
    </main>
  );
}
