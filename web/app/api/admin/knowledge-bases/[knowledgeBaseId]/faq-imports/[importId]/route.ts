import { getFAQImportStatus } from "@/lib/knowledge-admin-server";

export const dynamic = "force-dynamic";

type RouteContext = {
  params: Promise<{ knowledgeBaseId: string; importId: string }>;
};

export async function GET(_request: Request, context: RouteContext) {
  const { knowledgeBaseId, importId } = await context.params;
  return getFAQImportStatus(knowledgeBaseId, importId);
}
