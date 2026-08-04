import { retryMarkdownVersion } from "@/lib/knowledge-admin-server";

type RouteContext = {
  params: Promise<{ knowledgeBaseId: string; documentId: string; versionId: string }>;
};

export async function POST(_request: Request, context: RouteContext) {
  const { knowledgeBaseId, documentId, versionId } = await context.params;
  return retryMarkdownVersion(knowledgeBaseId, documentId, versionId);
}
