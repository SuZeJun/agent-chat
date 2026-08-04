import { importFAQs } from "@/lib/knowledge-admin-server";

const maxFAQUploadBytes = 2 << 20;
type RouteContext = { params: Promise<{ knowledgeBaseId: string }> };

export async function POST(request: Request, context: RouteContext) {
  const { knowledgeBaseId } = await context.params;
  let requestForm: FormData;
  try {
    requestForm = await request.formData();
  } catch {
    return Response.json(
      { error: { code: "invalid_faq_file", message: "请选择 CSV 文件" } },
      { status: 400 },
    );
  }
  const file = requestForm.get("file");
  if (!(file instanceof File) || file.size <= 0 || file.size > maxFAQUploadBytes) {
    return Response.json(
      {
        error: {
          code: "invalid_faq_file",
          message: "请选择不超过 2 MiB 的非空 CSV 文件",
        },
      },
      { status: 400 },
    );
  }
  const upstreamForm = new FormData();
  upstreamForm.append("file", file, file.name);
  return importFAQs(knowledgeBaseId, upstreamForm);
}
