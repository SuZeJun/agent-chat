"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { AssistantMessage } from "@/components/assistant-message";
import { Composer } from "@/components/composer";
import { Button } from "@/components/ui/button";
import { restoreMessageHistory, type ChatItem } from "@/lib/message-history";
import { initialAssistantState } from "@/lib/run-events";
import { useRunStream } from "@/lib/use-run-stream";
import type {
  ApiErrorBody,
  AssistantState,
  CreateConversationResponse,
  MessageHistoryResponse,
  SendMessageResponse,
} from "@/lib/types";

function conversationStorageKey(knowledgeBaseID: string): string {
  return `agent-chat:conversation-id:${knowledgeBaseID}`;
}

async function readError(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as ApiErrorBody;
    return body.error?.message || body.error?.code || `请求失败（${response.status}）`;
  } catch {
    return `请求失败（${response.status}）`;
  }
}

function validConversationID(value: string | null): value is string {
  return Boolean(value && value.trim() && value.length <= 64);
}

async function fetchHistory(
  conversationID: string,
  before?: string,
): Promise<Response> {
  const query = new URLSearchParams({ limit: "50" });
  if (before) {
    query.set("before", before);
  }
  return fetch(
    `/api/conversations/${encodeURIComponent(conversationID)}/messages?${query.toString()}`,
    { cache: "no-store" },
  );
}

type ChatPanelProps = {
  knowledgeBaseId: string;
  knowledgeBaseName: string;
};

type RunSubscriptionProps = {
  runId: string;
  onRunEvent: (
    runId: string,
    updater: (state: AssistantState) => AssistantState,
  ) => void;
  onRunSettled: (runId: string) => void;
};

function RunSubscription({ runId, onRunEvent, onRunSettled }: RunSubscriptionProps) {
  const handleEvent = useCallback(
    (updater: (state: AssistantState) => AssistantState) => {
      onRunEvent(runId, updater);
    },
    [onRunEvent, runId],
  );
  const handleSettled = useCallback(() => {
    onRunSettled(runId);
  }, [onRunSettled, runId]);

  useRunStream(runId, handleEvent, handleSettled);
  return null;
}

export function ChatPanel({ knowledgeBaseId, knowledgeBaseName }: ChatPanelProps) {
  const [items, setItems] = useState<ChatItem[]>([]);
  const [activeRunIds, setActiveRunIds] = useState<string[]>([]);
  const [nextBeforeMessageId, setNextBeforeMessageId] = useState<string | undefined>();
  const [restoring, setRestoring] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [restoreVersion, setRestoreVersion] = useState(0);
  const [restoreFailed, setRestoreFailed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const conversationIdRef = useRef<string | null>(null);
  const historyViewportRef = useRef<HTMLDivElement>(null);
  const previousHistoryHeightRef = useRef<number | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const storageKey = conversationStorageKey(knowledgeBaseId);

  useEffect(() => {
    if (previousHistoryHeightRef.current !== null && historyViewportRef.current) {
      historyViewportRef.current.scrollTop +=
        historyViewportRef.current.scrollHeight - previousHistoryHeightRef.current;
      previousHistoryHeightRef.current = null;
      return;
    }
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [items]);

  useEffect(() => {
    let cancelled = false;
    const restore = async () => {
      setRestoring(true);
      setRestoreFailed(false);
      setError(null);
      const stored = window.localStorage.getItem(storageKey);
      if (!validConversationID(stored)) {
        window.localStorage.removeItem(storageKey);
        conversationIdRef.current = null;
        if (!cancelled) {
          setRestoring(false);
        }
        return;
      }
      const conversationID = stored.trim();
      conversationIdRef.current = conversationID;
      if (conversationID !== stored) {
        window.localStorage.setItem(storageKey, conversationID);
      }
      try {
        const response = await fetchHistory(conversationID);
        if (response.status === 404) {
          window.localStorage.removeItem(storageKey);
          conversationIdRef.current = null;
          if (!cancelled) {
            setItems([]);
            setActiveRunIds([]);
            setNextBeforeMessageId(undefined);
          }
          return;
        }
        if (!response.ok) {
          throw new Error(await readError(response));
        }
        const page = (await response.json()) as MessageHistoryResponse;
        const restored = restoreMessageHistory(page);
        if (!cancelled) {
          setItems(restored.items);
          setActiveRunIds(restored.activeRunIds);
          setNextBeforeMessageId(restored.nextBeforeMessageId);
        }
      } catch (cause) {
        if (!cancelled) {
          setRestoreFailed(true);
          setError(cause instanceof Error ? cause.message : "恢复会话失败");
        }
      } finally {
        if (!cancelled) {
          setRestoring(false);
        }
      }
    };
    void restore();
    return () => {
      cancelled = true;
    };
  }, [restoreVersion, storageKey]);

  // 多标签页或 API 并发可能留下多个活动 Run，事件必须按 runId 定位消息。
  const updateRunAssistant = useCallback(
    (runId: string, updater: (state: AssistantState) => AssistantState) => {
      setItems((current) => {
        const index = current.findIndex(
          (item) => item.kind === "assistant" && item.state.runId === runId,
        );
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

  const handleRunSettled = useCallback((runId: string) => {
    setActiveRunIds((current) => current.filter((candidate) => candidate !== runId));
  }, []);

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
    window.localStorage.setItem(storageKey, conversation.id);
    return conversation.id;
  }, [storageKey]);

  const loadOlder = useCallback(async () => {
    const conversationID = conversationIdRef.current;
    if (!conversationID || !nextBeforeMessageId || loadingOlder) {
      return;
    }
    setLoadingOlder(true);
    setError(null);
    try {
      const response = await fetchHistory(conversationID, nextBeforeMessageId);
      if (!response.ok) {
        throw new Error(await readError(response));
      }
      const page = (await response.json()) as MessageHistoryResponse;
      const restored = restoreMessageHistory(page);
      previousHistoryHeightRef.current = historyViewportRef.current?.scrollHeight ?? null;
      setItems((current) => [...restored.items, ...current]);
      setActiveRunIds((current) => [
        ...new Set([...current, ...restored.activeRunIds]),
      ]);
      setNextBeforeMessageId(restored.nextBeforeMessageId);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "读取更早消息失败");
    } finally {
      setLoadingOlder(false);
    }
  }, [loadingOlder, nextBeforeMessageId]);

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
        setActiveRunIds((current) =>
          current.includes(message.runId) ? current : [...current, message.runId],
        );
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : "发送失败");
      }
    },
    [ensureConversation],
  );

  return (
    <div className="flex h-dvh flex-col">
      {activeRunIds.map((runId) => (
        <RunSubscription
          key={runId}
          runId={runId}
          onRunEvent={updateRunAssistant}
          onRunSettled={handleRunSettled}
        />
      ))}
      <header className="flex items-center justify-between border-b border-border px-4 py-3">
        <h1 className="text-sm font-semibold">Agent Chat</h1>
        <p className="text-xs text-muted-foreground">
          知识库：<span className="font-medium">{knowledgeBaseName}</span>
        </p>
      </header>

      <div ref={historyViewportRef} className="flex-1 space-y-4 overflow-y-auto p-4">
        {nextBeforeMessageId ? (
          <div className="flex justify-center">
            <Button variant="ghost" size="sm" disabled={loadingOlder} onClick={loadOlder}>
              {loadingOlder ? "正在读取…" : "加载更早消息"}
            </Button>
          </div>
        ) : null}

        {restoring ? (
          <p className="pt-16 text-center text-sm text-muted-foreground">正在恢复会话…</p>
        ) : items.length === 0 ? (
          <p className="pt-16 text-center text-sm text-muted-foreground">
            向企业知识库提问，回答会附带可核对的引用来源。
          </p>
        ) : null}

        {items.map((item) => {
          if (item.kind === "customer") {
            return (
              <div key={item.id} className="flex justify-end">
                <p className="max-w-[85%] rounded-lg bg-primary px-3 py-2 text-sm whitespace-pre-wrap text-primary-foreground">
                  {item.content}
                </p>
              </div>
            );
          }
          if (item.kind === "notice") {
            return (
              <p key={item.id} className="text-center text-xs text-muted-foreground">
                {item.content}
              </p>
            );
          }
          return (
            <div key={item.id} className="flex justify-start">
              <AssistantMessage state={item.state} />
            </div>
          );
        })}

        {error ? (
          <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
            <p>{error}</p>
            {restoreFailed ? (
              <Button
                className="mt-2"
                variant="outline"
                size="sm"
                onClick={() => setRestoreVersion((current) => current + 1)}
              >
                重试恢复
              </Button>
            ) : null}
          </div>
        ) : null}

        <div ref={bottomRef} />
      </div>

      <Composer
        disabled={restoring || restoreFailed || activeRunIds.length > 0}
        onSend={send}
      />
    </div>
  );
}
