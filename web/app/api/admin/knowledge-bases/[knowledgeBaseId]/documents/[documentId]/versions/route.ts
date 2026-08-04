import { readBoundedRequestBody } from "@/lib/bounded-request";
import { createMarkdownVersion } from "@/lib/knowledge-admin-server";

const maxMarkdownRequestBytes = (512 << 10) + (64 << 10);
type RouteContext = {
  params: Promise<{ knowledgeBaseId: string; documentId: string }>;
};

export async function POST(request: Request, context: RouteContext) {
  const { knowledgeBaseId, documentId } = await context.params;
  const body = await readBoundedRequestBody(request, maxMarkdownRequestBytes);
  if (body === null) {
    return Response.json(
      { error: { code: "markdown_request_too_large", message: "Markdown 内容不能超过 512 KiB" } },
      { status: 413 },
    );
  }
  return createMarkdownVersion(knowledgeBaseId, documentId, body.buffer as ArrayBuffer);
}
