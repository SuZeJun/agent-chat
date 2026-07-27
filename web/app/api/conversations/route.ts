import { readServerConfig } from "@/lib/config";

export const dynamic = "force-dynamic";

/** 创建绑定配置知识库的会话；演示身份由服务端注入。 */
export async function POST() {
  const config = readServerConfig();
  const upstream = await fetch(`${config.apiBaseUrl}/api/v1/conversations`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Customer-ID": config.customerId,
    },
    body: JSON.stringify({ knowledgeBaseId: config.knowledgeBaseId }),
    cache: "no-store",
  });

  const body = await upstream.text();
  return new Response(body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}
