import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  createMarkdownDocument,
  listMarkdownDocuments,
} from "@/lib/knowledge-admin-server";

import { GET, POST } from "./route";

vi.mock("@/lib/knowledge-admin-server", () => ({
  createMarkdownDocument: vi.fn(),
  listMarkdownDocuments: vi.fn(),
}));

const routeContext = {
  params: Promise.resolve({ knowledgeBaseId: "base /1" }),
};

describe("Markdown document BFF route", () => {
  beforeEach(() => {
    vi.mocked(createMarkdownDocument).mockReset();
    vi.mocked(listMarkdownDocuments).mockReset();
  });

  it("forwards list requests within the selected knowledge base", async () => {
    vi.mocked(listMarkdownDocuments).mockResolvedValue(Response.json({ items: [] }));

    const response = await GET(new Request("http://localhost/documents"), routeContext);

    expect(response.status).toBe(200);
    expect(listMarkdownDocuments).toHaveBeenCalledWith("base /1");
  });

  it("rejects an oversized Markdown body before forwarding", async () => {
    const request = new Request("http://localhost/documents", {
      method: "POST",
      headers: { "Content-Length": String(600 << 10) },
      body: new Uint8Array([1]),
    });

    const response = await POST(request, routeContext);
    const body = (await response.json()) as { error: { code: string; message: string } };

    expect(response.status).toBe(413);
    expect(body.error.code).toBe("markdown_request_too_large");
    expect(body.error.message).toContain("512 KiB");
    expect(createMarkdownDocument).not.toHaveBeenCalled();
  });

  it("forwards a bounded JSON body without trusting browser identity", async () => {
    vi.mocked(createMarkdownDocument).mockResolvedValue(
      Response.json({ id: "doc_1" }, { status: 201 }),
    );
    const request = new Request("http://localhost/documents", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: "API 指南", content: "# API" }),
    });

    const response = await POST(request, routeContext);

    expect(response.status).toBe(201);
    expect(createMarkdownDocument).toHaveBeenCalledWith("base /1", expect.any(ArrayBuffer));
  });
});
