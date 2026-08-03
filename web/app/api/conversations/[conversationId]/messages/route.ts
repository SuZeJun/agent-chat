import { readServerConfig } from "@/lib/config";

export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ conversationId: string }> };

/** 转发客户会话历史查询，并由服务端配置注入客户身份。 */
export async function GET(request: Request, context: RouteContext) {
  const { conversationId } = await context.params;
  const config = readServerConfig();
  const incoming = new URL(request.url);
  const upstreamURL = new URL(
    `${config.apiBaseUrl}/api/v1/conversations/${encodeURIComponent(conversationId)}/messages`,
  );
  for (const name of ["before", "limit"]) {
    const value = incoming.searchParams.get(name);
    if (value) {
      upstreamURL.searchParams.set(name, value);
    }
  }

  const upstream = await fetch(upstreamURL, {
    headers: { "X-Customer-ID": config.customerId },
    cache: "no-store",
  });
  const body = await upstream.text();
  return new Response(body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}

/**
 * 转发客户消息。
 *
 * clientMessageId 由浏览器生成并在重试间保持不变，后端据此做幂等去重，
 * 因此这里原样透传而不重新生成。
 */
export async function POST(request: Request, context: RouteContext) {
  const { conversationId } = await context.params;
  const config = readServerConfig();

  let payload: { clientMessageId?: unknown; content?: unknown };
  try {
    payload = await request.json();
  } catch {
    return Response.json(
      { error: { code: "invalid_request_body", message: "请求体必须是 JSON" } },
      { status: 400 },
    );
  }

  const clientMessageId = String(payload.clientMessageId ?? "").trim();
  const content = String(payload.content ?? "").trim();
  if (!clientMessageId || !content) {
    return Response.json(
      {
        error: {
          code: "invalid_request_body",
          message: "clientMessageId 与 content 均不能为空",
        },
      },
      { status: 400 },
    );
  }

  const upstream = await fetch(
    `${config.apiBaseUrl}/api/v1/conversations/${encodeURIComponent(conversationId)}/messages`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Customer-ID": config.customerId,
      },
      body: JSON.stringify({ clientMessageId, content }),
      cache: "no-store",
    },
  );

  const body = await upstream.text();
  return new Response(body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}
