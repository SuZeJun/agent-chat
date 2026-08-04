// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AgentHandoffWorkspace } from "@/components/agent-handoff-workspace";
import type { HandoffConversation, ConversationStatus } from "@/lib/types";

function detail(status: ConversationStatus, messages: HandoffConversation["messages"] = []): HandoffConversation {
  return {
    id: "conversation-1",
    customerId: "customer-1",
    knowledgeBaseId: "base-1",
    status,
    assignedAgentId: status === "human_active" ? "agent-1" : undefined,
    summary: {
      customerRequest: "账单导出失败",
      confirmedFacts: ["已确认导出按钮无响应"],
      unresolvedQuestions: ["是否所有账单都失败"],
      riskSignals: ["紧急请求"],
      citations: [{ sourceId: "S1", title: "导出说明", excerpt: "通常两分钟完成", documentId: "doc-1", versionId: "version-1" }],
      toolCalls: [{ name: "lookup_invoice", status: "completed" }],
      recommendedAction: "核对导出任务状态",
      createdAt: "2026-08-05T00:00:00Z",
      updatedAt: "2026-08-05T00:00:00Z",
    },
    messages,
    events: [],
  };
}

describe("AgentHandoffWorkspace", () => {
  beforeEach(() => {
    vi.useRealTimers();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("takes over once, sends a human reply, and removes the conversation after AI resume", async () => {
    let status: ConversationStatus = "waiting_human";
    let messages: HandoffConversation["messages"] = [];
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method?.toUpperCase() ?? "GET";
      if (url === "/api/agent/conversations" && method === "GET") {
        return new Response(JSON.stringify({ items: status === "ai_active" ? [] : [detail(status)] }), { status: 200 });
      }
      if (url === "/api/agent/conversations/conversation-1" && method === "GET") {
        return new Response(JSON.stringify(detail(status, messages)), { status: 200 });
      }
      if (url.endsWith("/takeover") && method === "POST") {
        status = "human_active";
        return new Response(JSON.stringify(detail(status, messages)), { status: 200 });
      }
      if (url.endsWith("/messages") && method === "POST") {
        messages = [{ id: "agent-message-1", role: "agent", content: "我来协助处理。", createdAt: "2026-08-05T00:00:01Z" }];
        return new Response(JSON.stringify(messages[0]), { status: 201 });
      }
      if (url.endsWith("/resume-ai") && method === "POST") {
        status = "ai_active";
        return new Response(JSON.stringify(detail(status, messages)), { status: 200 });
      }
      throw new Error(`unexpected request: ${method} ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<AgentHandoffWorkspace />);
    await waitFor(() => expect(screen.getByText("customer-1")).toBeTruthy());
    fireEvent.click(screen.getByText("customer-1"));
    await waitFor(() => expect(screen.getByText("账单导出失败")).toBeTruthy());
    expect(screen.getByText("导出说明")).toBeTruthy();
    expect(screen.getByText("lookup_invoice · completed")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "接管" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "恢复 AI" })).toBeTruthy());

    fireEvent.change(screen.getByPlaceholderText("输入人工回复"), { target: { value: "我来协助处理。" } });
    fireEvent.submit(screen.getByRole("button", { name: "发送" }).closest("form") as HTMLFormElement);
    await waitFor(() => expect(screen.getByText("我来协助处理。")).toBeTruthy());
    const messageCall = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith("/messages") && init?.method === "POST");
    expect(messageCall?.[1]?.body).toBe(JSON.stringify({ content: "我来协助处理。" }));

    fireEvent.click(screen.getByRole("button", { name: "恢复 AI" }));
    await waitFor(() => expect(screen.getByText("选择一条会话查看摘要和历史。")).toBeTruthy());
    expect(screen.queryByPlaceholderText("输入人工回复")).toBeNull();
  });
});
