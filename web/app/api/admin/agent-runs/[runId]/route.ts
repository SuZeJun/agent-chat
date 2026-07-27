import { readServerConfig } from "@/lib/config";

export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ runId: string }> };

/**
 * 读取一次 Agent Run 的 Trace。
 *
 * 独立于客户作用域的路由：Trace 面向管理员与 AI 运营人员，包含检索分数、
 * 判定理由和模型用量等内部信息，不应经由客户身份的路由取得。
 */
export async function GET(_request: Request, context: RouteContext) {
  const { runId } = await context.params;
  const config = readServerConfig();

  const upstream = await fetch(
    `${config.apiBaseUrl}/api/v1/admin/agent-runs/${encodeURIComponent(runId)}`,
    {
      headers: { "X-Admin-ID": config.adminId },
      cache: "no-store",
    },
  );

  const body = await upstream.text();
  return new Response(body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}
