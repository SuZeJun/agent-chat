import { getHandoffConversation } from "@/lib/handoff-server";

type RouteContext = { params: Promise<{ conversationId: string }> };

export async function GET(_request: Request, context: RouteContext) {
  const { conversationId } = await context.params;
  return getHandoffConversation(conversationId);
}
