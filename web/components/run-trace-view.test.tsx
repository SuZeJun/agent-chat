// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { RunTraceView } from "@/components/run-trace-view";
import type { RunTrace } from "@/lib/types";

afterEach(cleanup);

describe("RunTraceView", () => {
  it("shows real tool execution and approval state transitions", () => {
    const trace: RunTrace = {
      runId: "run_1",
      requestId: "request_1",
      conversationId: "conversation_1",
      question: "帮我建个工单",
      status: "completed",
      result: {
        answer: "工单草稿已生成，请确认。",
        nextAction: "confirm_ticket",
        nodePath: ["plan_tool", "execute_tool"],
        toolCalls: [
          {
            name: "draft_ticket",
            status: "succeeded",
            durationMillis: 12,
          },
        ],
      },
      steps: [],
      events: [
        {
          eventId: "event_required",
          runId: "run_1",
          sequence: 5,
          type: "approval.required",
          payload: { approvalId: "approval_1" },
          createdAt: "2026-08-04T00:00:00Z",
        },
        {
          eventId: "event_confirmed",
          runId: "run_1",
          sequence: 7,
          type: "approval.confirmed",
          payload: { approvalId: "approval_1", jobId: "job_1" },
          createdAt: "2026-08-04T00:01:00Z",
        },
        {
          eventId: "event_ticket",
          runId: "run_1",
          sequence: 8,
          type: "ticket.created",
          payload: { approvalId: "approval_1", ticketNumber: "TK-20260804-0001" },
          createdAt: "2026-08-04T00:01:01Z",
        },
      ],
      createdAt: "2026-08-04T00:00:00Z",
      completedAt: "2026-08-04T00:00:01Z",
    };

    render(<RunTraceView trace={trace} />);

    expect(screen.getByText("draft_ticket")).toBeTruthy();
    expect(screen.getByText("12 ms")).toBeTruthy();
    expect(screen.getByText("等待客户确认")).toBeTruthy();
    expect(screen.getByText("客户已确认")).toBeTruthy();
    expect(screen.getByText("工单已创建")).toBeTruthy();
    expect(screen.getByText("ticket=TK-20260804-0001")).toBeTruthy();
  });
});
