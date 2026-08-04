// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { FAQAdminPanel } from "@/components/faq-admin-panel";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const knowledgeBases = {
  items: [
    { id: "base_1", name: "产品知识", description: "当前产品 FAQ", status: "active" },
    { id: "base_2", name: "历史知识", description: "只读归档", status: "disabled" },
  ],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("FAQAdminPanel", () => {
  it("loads knowledge bases and switches the import scope", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>(async () => jsonResponse(knowledgeBases)));

    render(<FAQAdminPanel />);

    const select = (await screen.findByLabelText("目标知识库")) as HTMLSelectElement;
    await waitFor(() => expect(select.value).toBe("base_1"));
    expect(screen.getByText("当前产品 FAQ")).toBeTruthy();
    fireEvent.change(select, { target: { value: "base_2" } });
    expect(screen.getByText("只读归档")).toBeTruthy();
    expect(screen.getByRole("button", { name: "导入并开始索引" }).hasAttribute("disabled")).toBe(true);
  });

  it("uploads a CSV and polls until every row is ready", async () => {
    let statusCalls = 0;
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if (url === "/api/admin/knowledge-bases") {
        return jsonResponse(knowledgeBases);
      }
      if (init?.method === "POST") {
        return jsonResponse({ id: "imp_1", status: "pending", totalRows: 1 }, 202);
      }
      statusCalls += 1;
      if (statusCalls === 1) {
        return jsonResponse({
          id: "imp_1",
          sourceName: "faq.csv",
          status: "pending",
          totalRows: 1,
          readyRows: 0,
          failedRows: 0,
          createdAt: "2026-08-04T00:00:00Z",
          items: [
            { rowNumber: 2, documentId: "doc_1", versionId: "ver_1", status: "pending" },
          ],
        });
      }
      return jsonResponse({
        id: "imp_1",
        sourceName: "faq.csv",
        status: "ready",
        totalRows: 1,
        readyRows: 1,
        failedRows: 0,
        createdAt: "2026-08-04T00:00:00Z",
        items: [
          { rowNumber: 2, documentId: "doc_1", versionId: "ver_1", status: "ready" },
        ],
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<FAQAdminPanel />);
    await screen.findByText("当前产品 FAQ");
    const file = new File(["question,answer\n如何重置密码？,请在设置中重置。"], "faq.csv", {
      type: "text/csv",
    });
    fireEvent.change(screen.getByLabelText("FAQ CSV"), { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "导入并开始索引" }));

    await screen.findAllByText("等待索引");
    await waitFor(() => expect(screen.getByText("全部 FAQ 已完成索引并发布。")).toBeTruthy(), {
      timeout: 3_000,
    });
    expect(screen.getByText("doc_1")).toBeTruthy();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true);
    expect(statusCalls).toBeGreaterThanOrEqual(2);
  });

  it("rejects an invalid file before sending an import request", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(knowledgeBases));
    vi.stubGlobal("fetch", fetchMock);

    render(<FAQAdminPanel />);
    await screen.findByText("当前产品 FAQ");
    const file = new File(["not csv"], "faq.txt", { type: "text/plain" });
    fireEvent.change(screen.getByLabelText("FAQ CSV"), { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "导入并开始索引" }));

    await screen.findByText("请选择不超过 2 MiB 的非空 CSV 文件");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("shows a retry action when the knowledge base list fails", async () => {
    let calls = 0;
    const fetchMock = vi.fn<typeof fetch>(async () => {
      calls += 1;
      return calls === 1
        ? jsonResponse({ error: { code: "unavailable", message: "知识服务不可用" } }, 503)
        : jsonResponse(knowledgeBases);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<FAQAdminPanel />);
    await screen.findByText("知识服务不可用");
    fireEvent.click(screen.getByRole("button", { name: "重试" }));

    await screen.findByText("当前产品 FAQ");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
