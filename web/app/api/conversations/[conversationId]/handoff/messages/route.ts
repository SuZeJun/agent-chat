import { readBoundedRequestBody } from "@/lib/bounded-request";
import { sendHandoffCustomerMessage } from "@/lib/handoff-server";

type RouteContext = { params: Promise<{ conversationId: string }> };

export async function POST(request: Request, context: RouteContext) {
  const { conversationId } = await context.params;
  const body = await readBoundedRequestBody(request, 64 << 10);
  if (body === null) {
    return Response.json({ error: { code: "invalid_handoff_message", message: "消息过长" } }, { status: 413 });
  }
  return sendHandoffCustomerMessage(conversationId, body.buffer as ArrayBuffer);
}
