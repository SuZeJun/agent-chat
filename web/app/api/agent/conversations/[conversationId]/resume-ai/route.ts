import { resumeAI } from "@/lib/handoff-server";

type RouteContext = { params: Promise<{ conversationId: string }> };

export async function POST(_request: Request, context: RouteContext) {
  const { conversationId } = await context.params;
  return resumeAI(conversationId);
}
