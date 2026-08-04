import { readCustomerConversationEvents } from "@/lib/handoff-server";

type RouteContext = { params: Promise<{ conversationId: string }> };

export async function GET(request: Request, context: RouteContext) {
  const { conversationId } = await context.params;
  const after = new URL(request.url).searchParams.get("after") ?? "";
  return readCustomerConversationEvents(conversationId, after);
}
