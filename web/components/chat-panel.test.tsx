// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ChatPanel } from "@/components/chat-panel";

class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];

  readonly url: string;
  readyState = FakeEventSource.OPEN;
  onerror: ((event: Event) => void) | null = null;
  private readonly listeners = new Map<string, EventListener[]>();

  constructor(url: string | URL) {
    this.url = String(url);
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListener) {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  close() {
    this.readyState = FakeEventSource.CLOSED;
  }

  emit(type: string, data: Record<string, unknown>) {
    const message = new MessageEvent(type, { data: JSON.stringify(data) });
    for (const listener of this.listeners.get(type) ?? []) {
      listener(message);
    }
  }
}

function historyResponse() {
  return new Response(
    JSON.stringify({
      conversationStatus: "ai_active",
      items: [
        {
          id: "message_first",
          role: "customer",
          content: "第一个问题",
          runId: "run_first",
          runStatus: "running",
          createdAt: "2026-08-04T00:00:00Z",
        },
        {
          id: "message_second",
          role: "customer",
          content: "第二个问题",
          runId: "run_second",
          runStatus: "pending",
          createdAt: "2026-08-04T00:00:01Z",
        },
      ],
    }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}

describe("ChatPanel history restoration", () => {
  beforeEach(() => {
    window.localStorage.clear();
    FakeEventSource.instances = [];
    Object.defineProperty(window.HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("restores with one GET, creates no Run, and subscribes every active run", async () => {
    window.localStorage.setItem("agent-chat:conversation-id:base_1", "conversation_1");
    const fetchMock = vi.fn<typeof fetch>(async () => historyResponse());
    vi.stubGlobal("fetch", fetchMock);

    render(<ChatPanel knowledgeBaseId="base_1" knowledgeBaseName="测试知识库" />);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0][0])).toContain(
      "/api/conversations/conversation_1/messages?limit=50",
    );
    expect(
      fetchMock.mock.calls.some(([, init]) => init?.method?.toUpperCase() === "POST"),
    ).toBe(false);
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(2));
    expect(FakeEventSource.instances.map((source) => source.url).sort()).toEqual([
      "/api/agent-runs/run_first/events",
      "/api/agent-runs/run_second/events",
    ]);

    FakeEventSource.instances[0].emit("message.delta", {
      eventId: "event_delta",
      runId: "run_first",
      sequence: 2,
      type: "message.delta",
      payload: { delta: "第一个回答" },
      createdAt: "2026-08-04T00:00:02Z",
    });
    FakeEventSource.instances[0].emit("run.completed", {
      eventId: "event_completed",
      runId: "run_first",
      sequence: 3,
      type: "run.completed",
      payload: {},
      createdAt: "2026-08-04T00:00:03Z",
    });

    await waitFor(() => expect(screen.getByText("第一个回答")).toBeTruthy());
    const firstCustomer = screen.getByText("第一个问题").parentElement;
    const secondCustomer = screen.getByText("第二个问题").parentElement;
    expect(firstCustomer?.nextElementSibling?.textContent).toContain("第一个回答");
    expect(secondCustomer?.nextElementSibling?.textContent).toContain("正在排队…");
  });

  it("keeps the composer disabled when history restoration fails", async () => {
    window.localStorage.setItem("agent-chat:conversation-id:base_1", "conversation_1");
    const fetchMock = vi.fn(async () => {
      throw new Error("network unavailable");
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<ChatPanel knowledgeBaseId="base_1" knowledgeBaseName="测试知识库" />);

    await waitFor(() => expect(screen.getByText("network unavailable")).toBeTruthy());
    const textarea = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
    const sendButton = screen.getByRole("button", { name: "发送" }) as HTMLButtonElement;
    expect(textarea.disabled).toBe(true);
    expect(sendButton.disabled).toBe(true);
    fireEvent.submit(sendButton.closest("form") as HTMLFormElement);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("stops active AI runs after handoff and routes later messages to the human channel", async () => {
    window.localStorage.setItem("agent-chat:conversation-id:base_1", "conversation_1");
    vi.stubGlobal("crypto", { randomUUID: () => "client-human-message" });
    let historyReads = 0;
    const fetchMock: ReturnType<typeof vi.fn<typeof fetch>> = vi.fn<typeof fetch>(async (input, init): Promise<Response> => {
      const url = String(input);
      const method = init?.method?.toUpperCase() ?? "GET";
      if (url.includes("/messages?limit=50")) {
        historyReads += 1;
        return new Response(
          JSON.stringify({
            conversationStatus: historyReads > 1 ? "waiting_human" : "ai_active",
            items: historyReads > 1
              ? [{ id: "system-handoff", role: "system", content: "已为你转接人工支持，请稍候。", createdAt: "2026-08-05T00:00:01Z" }]
              : [{ id: "message-running", role: "customer", content: "需要帮助", runId: "run-active", runStatus: "running", createdAt: "2026-08-05T00:00:00Z" }],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (url.endsWith("/handoff") && method === "POST") {
        return new Response(JSON.stringify({ conversationId: "conversation_1", status: "waiting_human", summary: {} }), { status: 202 });
      }
      if (url.includes("/events?after=")) {
        return new Response(JSON.stringify({ conversationId: "conversation_1", status: "waiting_human", items: [] }), { status: 200 });
      }
      if (url.endsWith("/handoff/messages") && method === "POST") {
        return new Response(JSON.stringify({ id: "message-human", role: "customer", content: "补充信息", createdAt: "2026-08-05T00:00:02Z" }), { status: 201 });
      }
      throw new Error(`unexpected request: ${method} ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<ChatPanel knowledgeBaseId="base_1" knowledgeBaseName="测试知识库" />);
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    fireEvent.click(screen.getByRole("button", { name: "转人工" }));

    await waitFor(() => expect(screen.getByText("等待人工")).toBeTruthy());
    await waitFor(() => expect(FakeEventSource.instances[0].readyState).toBe(FakeEventSource.CLOSED));
    expect(screen.queryByText("正在排队…")).toBeNull();

    const textarea = screen.getByLabelText("输入问题");
    fireEvent.change(textarea, { target: { value: "补充信息" } });
    fireEvent.submit(screen.getByRole("button", { name: "发送" }).closest("form") as HTMLFormElement);
    await waitFor(() => {
      expect(fetchMock.mock.calls.some(([input, init]) =>
        String(input).endsWith("/handoff/messages") && init?.method === "POST",
      )).toBe(true);
    });
  });
});
