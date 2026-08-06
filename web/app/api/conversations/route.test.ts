import { beforeEach, describe, expect, it, vi } from "vitest";

import { readServerConfig } from "@/lib/config";
import { resolveDemoKnowledgeBase } from "@/lib/demo-knowledge-base-server";

import { POST } from "./route";

vi.mock("@/lib/config", () => ({
  readServerConfig: vi.fn(),
}));

vi.mock("@/lib/demo-knowledge-base-server", () => ({
  resolveDemoKnowledgeBase: vi.fn(),
}));

const serverConfig = {
  apiBaseUrl: "http://api:8080",
  customerId: "demo-customer",
  agentId: "demo-agent",
  adminId: "demo-admin",
  knowledgeBaseId: "",
  knowledgeBaseName: "Agent Chat SaaS 演示知识库",
};

describe("conversation BFF route", () => {
  beforeEach(() => {
    vi.mocked(readServerConfig).mockReset().mockReturnValue(serverConfig);
    vi.mocked(resolveDemoKnowledgeBase).mockReset();
    vi.stubGlobal("fetch", vi.fn());
  });

  it("forwards the server-resolved knowledge base when no explicit ID is configured", async () => {
    vi.mocked(resolveDemoKnowledgeBase).mockResolvedValue({
      id: "kb_resolved",
      name: serverConfig.knowledgeBaseName,
    });
    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify({ id: "conv_1" }), { status: 201 }),
    );

    const response = await POST();

    expect(response.status).toBe(201);
    const [url, init] = vi.mocked(fetch).mock.calls[0];
    expect(url).toBe("http://api:8080/api/v1/conversations");
    expect(JSON.parse(String(init?.body))).toEqual({ knowledgeBaseId: "kb_resolved" });
    expect(new Headers(init?.headers).get("X-Customer-ID")).toBe("demo-customer");
  });

  it("refuses to create a conversation when the demo knowledge base is unresolved", async () => {
    vi.mocked(resolveDemoKnowledgeBase).mockRejectedValue(
      new Error("demo knowledge base is missing or ambiguous"),
    );

    const response = await POST();
    const body = (await response.json()) as { error: { code: string } };

    expect(response.status).toBe(503);
    expect(body.error.code).toBe("demo_knowledge_base_unavailable");
    expect(fetch).not.toHaveBeenCalled();
  });
});
