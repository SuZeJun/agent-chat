import { readBoundedRequestBody } from "@/lib/bounded-request";
import { requestHandoff } from "@/lib/handoff-server";

type RouteContext = { params: Promise<{ conversationId: string }> };

export async function POST(request: Request, context: RouteContext) {
  const { conversationId } = await context.params;
  const body = await readBoundedRequestBody(request, 64 << 10);
  if (body === null) {
    return Response.json({ error: { code: "invalid_handoff_request", message: "转人工说明过长" } }, { status: 413 });
  }
  return requestHandoff(conversationId, body.buffer as ArrayBuffer);
}
