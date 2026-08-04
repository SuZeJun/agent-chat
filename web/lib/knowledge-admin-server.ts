import { readAdminServerConfig } from "@/lib/config";

const knowledgeUpstreamTimeoutMs = 10_000;

function upstreamFailure(code: string, message: string, status: number): Response {
  return Response.json({ error: { code, message } }, { status });
}

async function forwardKnowledgeRequest(
  path: string,
  init?: RequestInit,
): Promise<Response> {
  const config = readAdminServerConfig();
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), knowledgeUpstreamTimeoutMs);
  try {
    const upstream = await fetch(`${config.apiBaseUrl}${path}`, {
      ...init,
      headers: {
        ...init?.headers,
        "X-Admin-ID": config.adminId,
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
    if (controller.signal.aborted) {
      return upstreamFailure(
        "knowledge_upstream_timeout",
        "知识服务响应超时，请稍后重试",
        504,
      );
    }
    return upstreamFailure(
      "knowledge_upstream_unreachable",
      "无法连接知识服务，请稍后重试",
      502,
    );
  } finally {
    clearTimeout(timeout);
  }
}

/** listKnowledgeBases 只使用服务端管理员身份读取知识库。 */
export function listKnowledgeBases(): Promise<Response> {
  return forwardKnowledgeRequest("/api/v1/admin/knowledge-bases");
}

/** importFAQs 转发已在 BFF 校验大小的 CSV，不接受浏览器覆盖管理员身份。 */
export function importFAQs(knowledgeBaseId: string, form: FormData): Promise<Response> {
  return forwardKnowledgeRequest(
    `/api/v1/admin/knowledge-bases/${encodeURIComponent(knowledgeBaseId)}/faq-imports`,
    { method: "POST", body: form },
  );
}

/** getFAQImportStatus 在知识库作用域内读取一次导入的逐行状态。 */
export function getFAQImportStatus(
  knowledgeBaseId: string,
  importId: string,
): Promise<Response> {
  return forwardKnowledgeRequest(
    `/api/v1/admin/knowledge-bases/${encodeURIComponent(knowledgeBaseId)}/faq-imports/${encodeURIComponent(importId)}`,
  );
}
