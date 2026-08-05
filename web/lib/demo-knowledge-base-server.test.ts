import { afterEach, describe, expect, it, vi } from "vitest";

import type { ServerConfig } from "@/lib/config";
import { resolveDemoKnowledgeBase } from "@/lib/demo-knowledge-base-server";

const config: ServerConfig = {
  apiBaseUrl: "http://api.example.test",
  customerId: "demo-customer",
  agentId: "demo-agent",
  adminId: "demo-admin",
  knowledgeBaseId: "",
  knowledgeBaseName: "演示知识库",
};

describe("resolveDemoKnowledgeBase", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("优先使用服务端显式配置的知识库 ID", async () => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);

    await expect(
      resolveDemoKnowledgeBase({ ...config, knowledgeBaseId: "kb-fixed" }),
    ).resolves.toEqual({ id: "kb-fixed", name: "演示知识库" });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("按唯一名称解析 seed 创建的 active 知识库", async () => {
    const fetcher = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [
            { id: "kb-disabled", name: "演示知识库", status: "disabled" },
            { id: "kb-demo", name: "演示知识库", status: "active" },
          ],
        }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetcher);

    await expect(resolveDemoKnowledgeBase(config)).resolves.toEqual({
      id: "kb-demo",
      name: "演示知识库",
    });
    expect(fetcher).toHaveBeenCalledWith(
      "http://api.example.test/api/v1/admin/knowledge-bases",
      expect.objectContaining({
        headers: { "X-Admin-ID": "demo-admin" },
        cache: "no-store",
      }),
    );
  });

  it("拒绝缺失或重名的知识库，避免客户作用域不确定", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            items: [
              { id: "kb-1", name: "演示知识库", status: "active" },
              { id: "kb-2", name: "演示知识库", status: "active" },
            ],
          }),
          { status: 200 },
        ),
      ),
    );

    await expect(resolveDemoKnowledgeBase(config)).rejects.toThrow("ambiguous");
  });
});
