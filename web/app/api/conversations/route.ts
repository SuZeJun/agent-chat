import { readServerConfig } from "@/lib/config";
import { resolveDemoKnowledgeBase } from "@/lib/demo-knowledge-base-server";

export const dynamic = "force-dynamic";

/** 创建绑定配置知识库的会话；演示身份由服务端注入。 */
export async function POST() {
  const config = readServerConfig();

  // 与首页同源地在服务端确定作用域：显式 ID 缺省时按唯一名称解析 seed 创建的
  // 知识库。不接受浏览器传入的知识库 ID，避免客户越权访问其他知识库。
  let knowledgeBaseId: string;
  try {
    knowledgeBaseId = (await resolveDemoKnowledgeBase(config)).id;
  } catch {
    return Response.json(
      {
        error: {
          code: "demo_knowledge_base_unavailable",
          message: "演示知识库尚未就绪或存在同名歧义",
        },
      },
      { status: 503 },
    );
  }

  const upstream = await fetch(`${config.apiBaseUrl}/api/v1/conversations`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Customer-ID": config.customerId,
    },
    body: JSON.stringify({ knowledgeBaseId }),
    cache: "no-store",
  });

  const body = await upstream.text();
  return new Response(body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}
