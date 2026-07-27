"use client";

import { useEffect } from "react";

import { isTerminalEvent, reduceRunEvent } from "@/lib/run-events";
import type { AssistantState, RunEvent, RunEventType } from "@/lib/types";

const RUN_EVENT_TYPES: RunEventType[] = [
  "run.started",
  "run.status",
  "retrieval.completed",
  "answerability.decided",
  "message.delta",
  "message.citation",
  "run.completed",
  "run.failed",
];

/**
 * 订阅单个 Run 的事件流。
 *
 * 后端在 Run 进入终态后会主动关闭流，而 EventSource 遇到流结束会自动重连；
 * 因此必须在收到终态事件时显式 close()，否则会退化成持续的空轮询。
 */
export function useRunStream(
  runId: string | null,
  onEvent: (updater: (state: AssistantState) => AssistantState) => void,
  onSettled: () => void,
) {
  useEffect(() => {
    if (!runId) {
      return;
    }

    const source = new EventSource(`/api/agent-runs/${encodeURIComponent(runId)}/events`);
    let closed = false;

    const close = () => {
      if (!closed) {
        closed = true;
        source.close();
      }
    };

    const handle = (message: MessageEvent<string>) => {
      let event: RunEvent;
      try {
        event = JSON.parse(message.data) as RunEvent;
      } catch {
        return;
      }
      onEvent((state) => reduceRunEvent(state, event));
      if (isTerminalEvent(event.type)) {
        close();
        onSettled();
      }
    };

    for (const type of RUN_EVENT_TYPES) {
      source.addEventListener(type, handle as EventListener);
    }

    source.onerror = () => {
      // 已收到终态而关闭时不再提示；其余情况交由浏览器自动重连，
      // 重连会带上 Last-Event-ID，由后端从断点继续。
      if (source.readyState === EventSource.CLOSED) {
        close();
        onSettled();
      }
    };

    return () => {
      close();
    };
  }, [runId, onEvent, onSettled]);
}
