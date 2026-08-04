import { importFAQs } from "@/lib/knowledge-admin-server";
import { readBoundedRequestBody } from "@/lib/bounded-request";

const maxFAQUploadBytes = 2 << 20;
// multipart 边界和文件元数据需要少量额外空间；BFF 仍必须在解析 FormData 前限制总请求体。
const maxFAQRequestBytes = maxFAQUploadBytes + (64 << 10);
type RouteContext = { params: Promise<{ knowledgeBaseId: string }> };

function uploadTooLarge(): Response {
  return Response.json(
    {
      error: {
        code: "faq_upload_too_large",
        message: "CSV 上传请求不能超过 2 MiB",
      },
    },
    { status: 413 },
  );
}

export async function POST(request: Request, context: RouteContext) {
  const { knowledgeBaseId } = await context.params;
  const requestBody = await readBoundedRequestBody(request, maxFAQRequestBytes);
  if (requestBody === null) {
    return uploadTooLarge();
  }

  let requestForm: FormData;
  try {
    requestForm = await new Response(requestBody.buffer as ArrayBuffer, {
      headers: { "Content-Type": request.headers.get("content-type") ?? "" },
    }).formData();
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
