import { getMarkdownDocument } from "@/lib/knowledge-admin-server";

type RouteContext = {
  params: Promise<{ knowledgeBaseId: string; documentId: string }>;
};

export async function GET(_request: Request, context: RouteContext) {
  const { knowledgeBaseId, documentId } = await context.params;
  return getMarkdownDocument(knowledgeBaseId, documentId);
}
