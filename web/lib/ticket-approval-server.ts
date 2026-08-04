import { readServerConfig } from "@/lib/config";

type TicketApprovalAction = "confirm" | "cancel";

/**
 * forwardTicketApproval 由 Next.js 服务端注入演示客户身份并转发审批请求。
 *
 * 浏览器只提交审批 ID 和明确动作，不能覆盖客户身份，也不能传入草稿或幂等键。
 */
export async function forwardTicketApproval(
  approvalId: string,
  action?: TicketApprovalAction,
): Promise<Response> {
  const config = readServerConfig();
  const suffix = action ? `/${action}` : "";
  const upstream = await fetch(
    `${config.apiBaseUrl}/api/v1/ticket-approvals/${encodeURIComponent(approvalId)}${suffix}`,
    {
      method: action ? "POST" : "GET",
      headers: { "X-Customer-ID": config.customerId },
      cache: "no-store",
    },
  );
  const body = await upstream.text();
  return new Response(body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}
