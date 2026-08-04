import { forwardTicketApproval } from "@/lib/ticket-approval-server";

export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ approvalId: string }> };

/** 确认结构化工单草稿；执行状态完全以 Go API 返回为准。 */
export async function POST(_request: Request, context: RouteContext) {
  const { approvalId } = await context.params;
  return forwardTicketApproval(approvalId, "confirm");
}
