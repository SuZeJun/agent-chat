import { afterEach, describe, expect, it, vi } from "vitest";

import { forwardTicketApproval } from "@/lib/ticket-approval-server";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("forwardTicketApproval", () => {
  it("injects the configured customer identity without forwarding client scope", async () => {
    vi.stubEnv("API_BASE_URL", "http://api.example.test");
    vi.stubEnv("DEMO_CUSTOMER_ID", "customer_1");
    vi.stubEnv("KNOWLEDGE_BASE_ID", "base_1");
    const fetchMock = vi.fn<typeof fetch>(async () =>
      Response.json({ approvalId: "approval_1", status: "pending" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const response = await forwardTicketApproval("approval /1", "confirm");

    expect(response.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toBe(
      "http://api.example.test/api/v1/ticket-approvals/approval%20%2F1/confirm",
    );
    expect(init?.method).toBe("POST");
    expect(init?.headers).toEqual({ "X-Customer-ID": "customer_1" });
    expect(init?.signal).toBeInstanceOf(AbortSignal);
  });

  it("returns a stable 504 error when the approval service exceeds its timeout", async () => {
    vi.useFakeTimers();
    vi.stubEnv("KNOWLEDGE_BASE_ID", "base_1");
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          const error = new Error("aborted");
          error.name = "AbortError";
          reject(error);
        });
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const pending = forwardTicketApproval("approval_1");
    await vi.advanceTimersByTimeAsync(10_000);
    const response = await pending;
    const body = (await response.json()) as { error: { code: string } };

    expect(response.status).toBe(504);
    expect(body.error.code).toBe("ticket_approval_upstream_timeout");
  });
});
