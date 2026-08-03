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
});
