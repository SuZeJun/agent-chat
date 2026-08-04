import { readAgentServerConfig, readServerConfig } from "@/lib/config";

const handoffTimeoutMs = 10_000;

async function forward(
  path: string,
  identityHeader: "X-Customer-ID" | "X-Agent-ID",
  identity: string,
  init?: RequestInit,
): Promise<Response> {
  const apiBaseUrl =
    identityHeader === "X-Agent-ID"
      ? readAgentServerConfig().apiBaseUrl
      : readServerConfig().apiBaseUrl;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), handoffTimeoutMs);
  try {
    const upstream = await fetch(`${apiBaseUrl}${path}`, {
      ...init,
      headers: {
        ...init?.headers,
        [identityHeader]: identity,
      },
      cache: "no-store",
      signal: controller.signal,
    });
    const body = await upstream.text();
    return new Response(body, {
      status: upstream.status,
      headers: { "Content-Type": "application/json; charset=utf-8" },
    });
  } catch {
    return Response.json(
      {
        error: {
          code: controller.signal.aborted ? "handoff_upstream_timeout" : "handoff_upstream_unreachable",
          message: controller.signal.aborted ? "人工支持服务响应超时" : "无法连接人工支持服务",
        },
      },
      { status: controller.signal.aborted ? 504 : 502 },
    );
  } finally {
    clearTimeout(timeout);
  }
}

function customerIdentity(): string {
  return readServerConfig().customerId;
}

function agentIdentity(): string {
  return readAgentServerConfig().agentId;
}

function customerPath(conversationId: string, suffix: string): string {
  return `/api/v1/conversations/${encodeURIComponent(conversationId)}${suffix}`;
}

function agentPath(conversationId = "", suffix = ""): string {
  const resource = conversationId ? `/${encodeURIComponent(conversationId)}` : "";
  return `/api/v1/agent/conversations${resource}${suffix}`;
}

export function requestHandoff(conversationId: string, body: ArrayBuffer): Promise<Response> {
  return forward(customerPath(conversationId, "/handoff"), "X-Customer-ID", customerIdentity(), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
  });
}

export function sendHandoffCustomerMessage(conversationId: string, body: ArrayBuffer): Promise<Response> {
  return forward(customerPath(conversationId, "/handoff/messages"), "X-Customer-ID", customerIdentity(), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
  });
}

export function readCustomerConversationEvents(conversationId: string, after: string): Promise<Response> {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return forward(customerPath(conversationId, `/events${query}`), "X-Customer-ID", customerIdentity());
}

export function listHandoffQueue(): Promise<Response> {
  return forward(agentPath(), "X-Agent-ID", agentIdentity());
}

export function getHandoffConversation(conversationId: string): Promise<Response> {
  return forward(agentPath(conversationId), "X-Agent-ID", agentIdentity());
}

export function takeoverHandoff(conversationId: string): Promise<Response> {
  return forward(agentPath(conversationId, "/takeover"), "X-Agent-ID", agentIdentity(), { method: "POST" });
}

export function sendHandoffAgentMessage(conversationId: string, body: ArrayBuffer): Promise<Response> {
  return forward(agentPath(conversationId, "/messages"), "X-Agent-ID", agentIdentity(), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
  });
}

export function resumeAI(conversationId: string): Promise<Response> {
  return forward(agentPath(conversationId, "/resume-ai"), "X-Agent-ID", agentIdentity(), { method: "POST" });
}

export function readAgentConversationEvents(conversationId: string, after: string): Promise<Response> {
  const query = after ? `?after=${encodeURIComponent(after)}` : "";
  return forward(agentPath(conversationId, `/events${query}`), "X-Agent-ID", agentIdentity());
}
