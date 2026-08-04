import { readServerConfig } from "@/lib/config";

type TicketApprovalAction = "confirm" | "cancel";
const approvalUpstreamTimeoutMs = 10_000;

function upstreamFailure(code: string, message: string, status: number): Response {
  return Response.json({ error: { code, message } }, { status });
}

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
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), approvalUpstreamTimeoutMs);
  try {
    const upstream = await fetch(
      `${config.apiBaseUrl}/api/v1/ticket-approvals/${encodeURIComponent(approvalId)}${suffix}`,
      {
        method: action ? "POST" : "GET",
        headers: { "X-Customer-ID": config.customerId },
        cache: "no-store",
        signal: controller.signal,
      },
    );
    const body = await upstream.text();
    return new Response(body, {
      status: upstream.status,
      headers: { "Content-Type": "application/json; charset=utf-8" },
    });
  } catch {
    if (controller.signal.aborted) {
      return upstreamFailure(
        "ticket_approval_upstream_timeout",
        "审批服务响应超时，请稍后重试",
        504,
      );
    }
    return upstreamFailure(
      "ticket_approval_upstream_unreachable",
      "无法连接审批服务，请稍后重试",
      502,
    );
  } finally {
    clearTimeout(timeout);
  }
}
