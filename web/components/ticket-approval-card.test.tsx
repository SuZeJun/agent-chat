// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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

    await waitFor(() => expect(screen.getByText("TK-20260804-0001")).toBeTruthy());
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
});
