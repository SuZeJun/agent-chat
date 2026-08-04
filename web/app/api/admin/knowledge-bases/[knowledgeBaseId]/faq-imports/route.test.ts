import { beforeEach, describe, expect, it, vi } from "vitest";

import { importFAQs } from "@/lib/knowledge-admin-server";

import { POST } from "./route";

vi.mock("@/lib/knowledge-admin-server", () => ({
  importFAQs: vi.fn(),
}));

const routeContext = {
  params: Promise.resolve({ knowledgeBaseId: "base_1" }),
};

describe("FAQ import BFF route", () => {
  beforeEach(() => {
    vi.mocked(importFAQs).mockReset();
  });

  it("rejects a declared oversized request before parsing multipart data", async () => {
    const request = new Request("http://localhost/api/admin/knowledge-bases/base_1/faq-imports", {
      method: "POST",
      headers: {
        "Content-Length": String(3 << 20),
        "Content-Type": "multipart/form-data; boundary=invalid",
      },
      body: new Uint8Array([1]),
    });

    const response = await POST(request, routeContext);
    const body = (await response.json()) as { error: { code: string } };

    expect(response.status).toBe(413);
    expect(body.error.code).toBe("faq_upload_too_large");
    expect(importFAQs).not.toHaveBeenCalled();
  });

  it("stops an undeclared oversized stream before FormData parsing", async () => {
    const request = new Request("http://localhost/api/admin/knowledge-bases/base_1/faq-imports", {
      method: "POST",
      body: new Uint8Array((2 << 20) + (64 << 10) + 1),
    });

    const response = await POST(request, routeContext);

    expect(response.status).toBe(413);
    expect(importFAQs).not.toHaveBeenCalled();
  });

  it("forwards a valid CSV upload", async () => {
    vi.mocked(importFAQs).mockResolvedValue(Response.json({ id: "import_1" }, { status: 202 }));
    const form = new FormData();
    form.append("file", new File(["question,answer\n问题,答案"], "faq.csv", { type: "text/csv" }));
    const request = new Request("http://localhost/api/admin/knowledge-bases/base_1/faq-imports", {
      method: "POST",
      body: form,
    });

    const response = await POST(request, routeContext);

    expect(response.status).toBe(202);
    expect(importFAQs).toHaveBeenCalledOnce();
    expect(importFAQs).toHaveBeenCalledWith("base_1", expect.any(FormData));
  });
});
