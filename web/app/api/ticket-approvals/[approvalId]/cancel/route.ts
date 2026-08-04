import { forwardTicketApproval } from "@/lib/ticket-approval-server";

export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ approvalId: string }> };

/** 取消结构化工单草稿；取消后不会创建工单。 */
export async function POST(_request: Request, context: RouteContext) {
  const { approvalId } = await context.params;
  return forwardTicketApproval(approvalId, "cancel");
}
