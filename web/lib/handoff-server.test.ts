import { afterEach, describe, expect, it, vi } from "vitest";

import { listHandoffQueue, requestHandoff } from "@/lib/handoff-server";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("handoff server forwarding", () => {
  it("injects the configured customer identity into handoff requests", async () => {
    vi.stubEnv("API_BASE_URL", "http://api.example.test");
    vi.stubEnv("DEMO_CUSTOMER_ID", "customer-1");
    vi.stubEnv("KNOWLEDGE_BASE_ID", "base-1");
    const fetchMock = vi.fn<typeof fetch>(async () => Response.json({ status: "waiting_human" }, { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);

    const response = await requestHandoff("conversation /1", new TextEncoder().encode(`{"reason":"人工"}`).buffer);

    expect(response.status).toBe(202);
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toBe("http://api.example.test/api/v1/conversations/conversation%20%2F1/handoff");
    expect(init?.method).toBe("POST");
    expect(init?.headers).toEqual({ "Content-Type": "application/json", "X-Customer-ID": "customer-1" });
  });

  it("uses only the configured server-side agent identity for queue access", async () => {
    vi.stubEnv("API_BASE_URL", "http://api.example.test");
    vi.stubEnv("DEMO_AGENT_ID", "agent-1");
    const fetchMock = vi.fn<typeof fetch>(async () => Response.json({ items: [] }));
    vi.stubGlobal("fetch", fetchMock);

    await listHandoffQueue();

    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toBe("http://api.example.test/api/v1/agent/conversations");
    expect(init?.headers).toEqual({ "X-Agent-ID": "agent-1" });
  });
});
