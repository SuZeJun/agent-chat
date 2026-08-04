import { forwardTicketApproval } from "@/lib/ticket-approval-server";

export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ approvalId: string }> };

/** 读取客户所属审批的权威状态。 */
export async function GET(_request: Request, context: RouteContext) {
  const { approvalId } = await context.params;
  return forwardTicketApproval(approvalId);
}
