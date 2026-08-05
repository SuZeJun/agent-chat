import type { ServerConfig } from "@/lib/config";

type KnowledgeBaseItem = {
  id: string;
  name: string;
  status: string;
};

type KnowledgeBaseList = {
  items?: KnowledgeBaseItem[];
};

export type ResolvedKnowledgeBase = {
  id: string;
  name: string;
};

/** resolveDemoKnowledgeBase 在服务端确定客户聊天的知识库作用域。 */
export async function resolveDemoKnowledgeBase(
  config: ServerConfig,
): Promise<ResolvedKnowledgeBase> {
  if (config.knowledgeBaseId) {
    return { id: config.knowledgeBaseId, name: config.knowledgeBaseName };
  }

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10_000);
  try {
    const response = await fetch(`${config.apiBaseUrl}/api/v1/admin/knowledge-bases`, {
      headers: { "X-Admin-ID": config.adminId },
      cache: "no-store",
      signal: controller.signal,
    });
    if (!response.ok) {
      throw new Error("knowledge base lookup failed");
    }
    const payload = (await response.json()) as KnowledgeBaseList;
    const matches = (payload.items || []).filter(
      (item) => item.name === config.knowledgeBaseName && item.status === "active",
    );
    if (matches.length !== 1 || !matches[0].id) {
      throw new Error("demo knowledge base is missing or ambiguous");
    }
    return { id: matches[0].id, name: matches[0].name };
  } finally {
    clearTimeout(timeout);
  }
}
