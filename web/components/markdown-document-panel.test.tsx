// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { MarkdownDocumentPanel } from "@/components/markdown-document-panel";

const base = { id: "base_1", name: "产品知识", description: "", status: "active" as const };
const document = {
  id: "doc_1",
  knowledgeBaseId: "base_1",
  title: "API 指南",
  latestVersion: 1,
  activeVersionId: "ver_1",
  versions: [
    {
      id: "ver_1",
      number: 1,
      status: "ready",
      active: true,
      createdAt: "2026-08-05T00:00:00Z",
    },
  ],
  createdAt: "2026-08-05T00:00:00Z",
  updatedAt: "2026-08-05T00:00:00Z",
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("MarkdownDocumentPanel", () => {
  it("creates a Markdown document and refreshes its pending version", async () => {
    let listCalls = 0;
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) => {
      if (init?.method === "POST") return jsonResponse(document, 201);
      listCalls += 1;
      return jsonResponse({ items: listCalls === 1 ? [] : [{ ...document, activeVersionId: undefined, versions: [{ ...document.versions[0], status: "pending", active: false }] }] });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<MarkdownDocumentPanel base={base} />);

    await screen.findByText("当前知识库还没有 Markdown 文档。");
    fireEvent.change(screen.getByPlaceholderText("文档标题"), { target: { value: "API 指南" } });
    fireEvent.change(screen.getByLabelText("Markdown 内容"), { target: { value: "# API\n\n接入说明" } });
    fireEvent.click(screen.getByRole("button", { name: "保存并开始索引" }));

    await screen.findByText("API 指南");
    expect(screen.getByText("等待索引")).toBeTruthy();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true);
  });

  it("loads the latest content and creates a new immutable version", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if (init?.method === "POST") return jsonResponse(document, 202);
      if (url.endsWith("/doc_1")) return jsonResponse({ ...document, latestContent: "# API\n\n第一版" });
      return jsonResponse({ items: [document] });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<MarkdownDocumentPanel base={base} />);

    await screen.findByText("API 指南");
    fireEvent.click(screen.getByRole("button", { name: "创建新版本" }));
    const editor = await screen.findByLabelText("新版本 Markdown 内容");
    expect((editor as HTMLTextAreaElement).value).toBe("# API\n\n第一版");
    fireEvent.change(editor, { target: { value: "# API\n\n第二版" } });
    fireEvent.click(screen.getByRole("button", { name: "提交新版本" }));

    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith("/versions") && init?.method === "POST")).toBe(true),
    );
  });

  it("requeues a failed version and exposes retry for list errors", async () => {
    let listCalls = 0;
    const failed = { ...document, activeVersionId: undefined, versions: [{ ...document.versions[0], status: "failed", active: false, errorCode: "embedding_failed" }] };
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) => {
      if (init?.method === "POST") return jsonResponse(failed, 202);
      listCalls += 1;
      if (listCalls === 1) return jsonResponse({ error: { code: "unavailable", message: "文档服务不可用" } }, 503);
      return jsonResponse({ items: [failed] });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<MarkdownDocumentPanel base={base} />);

    await screen.findByText("文档服务不可用");
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    await screen.findByText("embedding_failed");
    fireEvent.click(screen.getByRole("button", { name: "重试" }));

    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith("/retry") && init?.method === "POST")).toBe(true));
  });
});
