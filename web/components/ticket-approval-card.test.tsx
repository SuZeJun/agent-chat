// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TicketApprovalCard } from "@/components/ticket-approval-card";
import type { TicketApproval, TicketApprovalPrompt } from "@/lib/types";

const prompt: TicketApprovalPrompt = {
  approvalId: "approval_1",
  draft: {
    title: "无法导出账单",
    description: "点击导出后没有响应。",
    priority: "high",
  },
  expiresAt: "2026-08-04T01:00:00Z",
};

function approvalResponse(
  overrides: Partial<TicketApproval> = {},
  status = 200,
): Response {
  const approval: TicketApproval = {
    approvalId: prompt.approvalId,
    status: "pending",
    draft: prompt.draft,
    executionStatus: "awaiting_confirmation",
    ...overrides,
  };
  return new Response(JSON.stringify(approval), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("TicketApprovalCard", () => {
  it("renders the structured draft and creates only one ticket after confirmation", async () => {
    let getCalls = 0;
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) => {
      if (init?.method === "POST") {
        return approvalResponse({ status: "approved", executionStatus: "pending" }, 202);
      }
      getCalls += 1;
      if (getCalls === 1) {
        return approvalResponse();
      }
      return approvalResponse({
        status: "approved",
        executionStatus: "succeeded",
        ticket: { id: "ticket_1", number: "TK-20260804-0001" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<TicketApprovalCard prompt={prompt} />);

    expect(screen.getByText("无法导出账单")).toBeTruthy();
    expect(screen.getByText("点击导出后没有响应。")).toBeTruthy();
    expect(screen.getByText("高")).toBeTruthy();
    const confirm = screen.getByRole("button", { name: "确认创建" }) as HTMLButtonElement;
    await waitFor(() => expect(confirm.disabled).toBe(false));

    fireEvent.click(confirm);
    fireEvent.click(confirm);

    await waitFor(() => expect(screen.getByText("TK-20260804-0001")).toBeTruthy(), {
      timeout: 3_000,
    });
    const postCalls = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(postCalls).toHaveLength(1);
    expect(String(postCalls[0][0])).toContain("/api/ticket-approvals/approval_1/confirm");
  });

  it("shows the authoritative cancelled state and makes no confirmation request", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) => {
      if (init?.method === "POST") {
        return approvalResponse({ status: "cancelled", executionStatus: "cancelled" });
      }
      return approvalResponse();
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<TicketApprovalCard prompt={prompt} />);
    const cancel = screen.getByRole("button", { name: "取消" }) as HTMLButtonElement;
    await waitFor(() => expect(cancel.disabled).toBe(false));
    fireEvent.click(cancel);

    await waitFor(() => expect(screen.getByText("已取消，未创建工单。")).toBeTruthy());
    const postCalls = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(postCalls).toHaveLength(1);
    expect(String(postCalls[0][0])).toContain("/api/ticket-approvals/approval_1/cancel");
  });

  it("turns a 410 response into an actionable expiration message", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) => {
      if (init?.method === "POST") {
        return new Response(
          JSON.stringify({
            error: {
              code: "ticket_approval_expired",
              message: "ticket approval has expired",
            },
          }),
          { status: 410, headers: { "Content-Type": "application/json" } },
        );
      }
      return approvalResponse();
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<TicketApprovalCard prompt={prompt} />);
    const confirm = screen.getByRole("button", { name: "确认创建" }) as HTMLButtonElement;
    await waitFor(() => expect(confirm.disabled).toBe(false));
    fireEvent.click(confirm);

    await waitFor(() =>
      expect(screen.getByText(/确认已过期.*重新发送/)).toBeTruthy(),
    );
    expect(screen.queryByRole("button", { name: "确认创建" })).toBeNull();
  });

  it("stops automatic polling after the bounded retry schedule", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn<typeof fetch>(async () =>
      approvalResponse({ status: "approved", executionStatus: "pending" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    render(<TicketApprovalCard prompt={prompt} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });

    expect(screen.getByText("工单仍在创建，自动刷新已暂停。")).toBeTruthy();
    expect(screen.getByRole("button", { name: "刷新状态" })).toBeTruthy();
    const callsAtStop = fetchMock.mock.calls.length;
    expect(callsAtStop).toBeGreaterThan(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(fetchMock).toHaveBeenCalledTimes(callsAtStop);
  });

  it("ignores an older pending response after a newer refresh succeeds", async () => {
    vi.useFakeTimers();
    let getCalls = 0;
    let resolveStale: ((response: Response) => void) | undefined;
    const staleResponse = new Promise<Response>((resolve) => {
      resolveStale = resolve;
    });
    const fetchMock = vi.fn<typeof fetch>(async () => {
      getCalls += 1;
      if (getCalls === 1) {
        return approvalResponse({ status: "approved", executionStatus: "pending" });
      }
      if (getCalls === 2) {
        throw new Error("temporary network error");
      }
      if (getCalls === 3) {
        // 故意忽略 AbortSignal，模拟代理已经返回但旧响应在浏览器中延迟交付。
        return staleResponse;
      }
      return approvalResponse({
        status: "approved",
        executionStatus: "succeeded",
        ticket: { id: "ticket_1", number: "TK-NEWER" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<TicketApprovalCard prompt={prompt} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText("TK-NEWER")).toBeTruthy();

    await act(async () => {
      resolveStale?.(
        approvalResponse({ status: "approved", executionStatus: "pending" }),
      );
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText("TK-NEWER")).toBeTruthy();
    expect(screen.queryByText("已确认，正在创建工单…")).toBeNull();
  });
});
