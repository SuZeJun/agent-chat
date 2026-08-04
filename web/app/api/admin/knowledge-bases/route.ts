import { listKnowledgeBases } from "@/lib/knowledge-admin-server";

export const dynamic = "force-dynamic";

export async function GET() {
  return listKnowledgeBases();
}
