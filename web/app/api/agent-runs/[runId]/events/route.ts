import { readServerConfig } from "@/lib/config";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

type RouteContext = { params: Promise<{ runId: string }> };

/**
 * 透传 Run 事件的 SSE 流。
 *
 * 直接把上游响应体作为流返回，不做缓冲，否则事件会被攒到最后一次性送达，
 * 处理进度就失去意义。请求的 AbortSignal 一并向上游传递，浏览器断开时
 * 后端轮询也随之结束。
 */
export async function GET(request: Request, context: RouteContext) {
  const { runId } = await context.params;
  const config = readServerConfig();

  const headers: Record<string, string> = {
    "X-Customer-ID": config.customerId,
    Accept: "text/event-stream",
  };
  // EventSource 断线重连时由浏览器自动带上，转发后后端可从断点续传。
  const lastEventId = request.headers.get("last-event-id");
  if (lastEventId) {
    headers["Last-Event-ID"] = lastEventId;
  }

  let upstream: Response;
  try {
    upstream = await fetch(
      `${config.apiBaseUrl}/api/v1/agent-runs/${encodeURIComponent(runId)}/events`,
      { headers, cache: "no-store", signal: request.signal },
    );
  } catch {
    return Response.json(
      { error: { code: "upstream_unreachable", message: "无法连接后端服务" } },
      { status: 502 },
    );
  }

  if (!upstream.ok || !upstream.body) {
    const body = await upstream.text();
    return new Response(body || "{}", {
      status: upstream.status,
      headers: { "Content-Type": "application/json; charset=utf-8" },
    });
  }

  return new Response(upstream.body, {
    status: 200,
    headers: {
      "Content-Type": "text/event-stream; charset=utf-8",
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    },
  });
}
