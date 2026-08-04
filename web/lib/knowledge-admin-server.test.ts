import { afterEach, describe, expect, it, vi } from "vitest";

import {
  getFAQImportStatus,
  importFAQs,
  listKnowledgeBases,
} from "@/lib/knowledge-admin-server";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("knowledge admin server proxy", () => {
  it("injects admin identity and encodes resource identifiers", async () => {
    vi.stubEnv("API_BASE_URL", "http://api.example.test");
    vi.stubEnv("DEMO_ADMIN_ID", "admin_1");
    vi.stubEnv("KNOWLEDGE_BASE_ID", "base_1");
    const fetchMock = vi.fn<typeof fetch>(async () => Response.json({ status: "pending" }));
    vi.stubGlobal("fetch", fetchMock);

    await listKnowledgeBases();
    await getFAQImportStatus("base /1", "import /1");
    const form = new FormData();
    form.append("file", new Blob(["question,answer\n问题,答案"]), "faq.csv");
    await importFAQs("base /1", form);

    expect(String(fetchMock.mock.calls[0][0])).toBe(
      "http://api.example.test/api/v1/admin/knowledge-bases",
    );
    expect(fetchMock.mock.calls[0][1]?.headers).toEqual({ "X-Admin-ID": "admin_1" });
    expect(String(fetchMock.mock.calls[1][0])).toContain(
      "/base%20%2F1/faq-imports/import%20%2F1",
    );
    expect(fetchMock.mock.calls[2][1]?.method).toBe("POST");
    expect(fetchMock.mock.calls[2][1]?.body).toBe(form);
    expect(fetchMock.mock.calls[2][1]?.signal).toBeInstanceOf(AbortSignal);
  });

  it("returns a stable timeout response", async () => {
    vi.useFakeTimers();
    vi.stubEnv("KNOWLEDGE_BASE_ID", "base_1");
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (_input, init) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
        }),
      ),
    );

    const pending = listKnowledgeBases();
    await vi.advanceTimersByTimeAsync(10_000);
    const response = await pending;
    const body = (await response.json()) as { error: { code: string } };

    expect(response.status).toBe(504);
    expect(body.error.code).toBe("knowledge_upstream_timeout");
  });
});
