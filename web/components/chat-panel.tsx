"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { AssistantMessage } from "@/components/assistant-message";
import { Composer } from "@/components/composer";
import { initialAssistantState } from "@/lib/run-events";
import { useRunStream } from "@/lib/use-run-stream";
import type {
  ApiErrorBody,
  AssistantState,
  CreateConversationResponse,
  SendMessageResponse,
} from "@/lib/types";

type ChatItem =
  | { kind: "customer"; id: string; content: string }
  | { kind: "assistant"; id: string; state: AssistantState };

async function readError(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as ApiErrorBody;
    return body.error?.message || body.error?.code || `请求失败（${response.status}）`;
  } catch {
    return `请求失败（${response.status}）`;
  }
}

export function ChatPanel({ knowledgeBaseName }: { knowledgeBaseName: string }) {
  const [items, setItems] = useState<ChatItem[]>([]);
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const conversationIdRef = useRef<string | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [items]);

  // 事件只更新最后一条助手消息：同一时刻至多存在一个进行中的 Run。
  const updateActiveAssistant = useCallback(
    (updater: (state: AssistantState) => AssistantState) => {
      setItems((current) => {
        const index = current.findLastIndex((item) => item.kind === "assistant");
        if (index < 0) {
          return current;
        }
        const target = current[index] as Extract<ChatItem, { kind: "assistant" }>;
        const next = [...current];
        next[index] = { ...target, state: updater(target.state) };
        return next;
      });
    },
    [],
  );

  const handleSettled = useCallback(() => setActiveRunId(null), []);

  useRunStream(activeRunId, updateActiveAssistant, handleSettled);

  const ensureConversation = useCallback(async (): Promise<string> => {
    if (conversationIdRef.current) {
      return conversationIdRef.current;
    }
    const response = await fetch("/api/conversations", { method: "POST" });
    if (!response.ok) {
      throw new Error(await readError(response));
    }
    const conversation = (await response.json()) as CreateConversationResponse;
    conversationIdRef.current = conversation.id;
    return conversation.id;
  }, []);

  const send = useCallback(
    async (content: string) => {
      setError(null);
      const clientMessageId = crypto.randomUUID();
      setItems((current) => [
        ...current,
        { kind: "customer", id: clientMessageId, content },
      ]);

      try {
        const conversationId = await ensureConversation();
        const response = await fetch(
          `/api/conversations/${encodeURIComponent(conversationId)}/messages`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ clientMessageId, content }),
          },
        );
        if (!response.ok) {
          throw new Error(await readError(response));
        }
        const message = (await response.json()) as SendMessageResponse;
        setItems((current) => [
          ...current,
          {
            kind: "assistant",
            id: message.runId,
            state: initialAssistantState(message.runId),
          },
        ]);
        setActiveRunId(message.runId);
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "发送失败");
      }
    },
    [ensureConversation],
  );

  return (
    <div className="flex h-dvh flex-col">
      <header className="flex items-center justify-between border-b border-border px-4 py-3">
        <h1 className="text-sm font-semibold">Agent Chat</h1>
        <p className="text-xs text-muted-foreground">
          知识库：<span className="font-medium">{knowledgeBaseName}</span>
        </p>
      </header>

      <div className="flex-1 space-y-4 overflow-y-auto p-4">
        {items.length === 0 ? (
          <p className="pt-16 text-center text-sm text-muted-foreground">
            向企业知识库提问，回答会附带可核对的引用来源。
          </p>
        ) : null}

        {items.map((item) =>
          item.kind === "customer" ? (
            <div key={item.id} className="flex justify-end">
              <p className="max-w-[85%] rounded-lg bg-primary px-3 py-2 text-sm whitespace-pre-wrap text-primary-foreground">
                {item.content}
              </p>
            </div>
          ) : (
            <div key={item.id} className="flex justify-start">
              <AssistantMessage state={item.state} />
            </div>
          ),
        )}

        {error ? (
          <p className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
            {error}
          </p>
        ) : null}

        <div ref={bottomRef} />
      </div>

      <Composer disabled={activeRunId !== null} onSend={send} />
    </div>
  );
}
