import { initialAssistantState } from "@/lib/run-events";
import type {
  AssistantState,
  MessageHistoryItem,
  MessageHistoryResponse,
} from "@/lib/types";

export type ChatItem =
  | { kind: "customer"; id: string; content: string }
  | { kind: "assistant"; id: string; state: AssistantState }
  | { kind: "notice"; id: string; content: string };

export type RestoredHistory = {
  items: ChatItem[];
  activeRunIds: string[];
  nextBeforeMessageId?: string;
};

function completedAssistant(item: MessageHistoryItem): AssistantState {
  const assessment = item.result?.assessment;
  const approval =
    item.result?.approvalId && item.result.ticketDraft
      ? {
          approvalId: item.result.approvalId,
          draft: item.result.ticketDraft,
          expiresAt: item.result.approvalExpiresAt,
        }
      : undefined;
  return {
    ...initialAssistantState(item.runId ?? item.id),
    stage: "completed",
    answer: item.content,
    decision: assessment?.decision,
    reason: assessment?.reason,
    confidence: assessment?.confidence,
    nextAction: item.result?.nextAction,
    evidence: assessment?.evidence ?? [],
    citations: item.result?.citations ?? [],
    approval,
  };
}

/**
 * 将持久化历史恢复为聊天 UI 状态。
 *
 * pending/running Run 只恢复 EventSource 订阅，不发送消息，因此页面刷新不会创建
 * 第二个 Agent Run。completed Run 直接使用持久化 Result 恢复引用与三分支呈现。
 */
export function restoreMessageHistory(page: MessageHistoryResponse): RestoredHistory {
  const assistantRuns = new Set(
    page.items
      .filter((item) => item.role === "assistant" && item.runId)
      .map((item) => item.runId as string),
  );
  const items: ChatItem[] = [];
  const activeRunIds: string[] = [];

  for (const item of page.items) {
    if (item.role === "customer") {
      items.push({ kind: "customer", id: item.id, content: item.content });
      if (!item.runId || assistantRuns.has(item.runId)) {
        continue;
      }
      if (item.runStatus === "pending" || item.runStatus === "running") {
        items.push({
          kind: "assistant",
          id: item.runId,
          state: initialAssistantState(item.runId),
        });
        activeRunIds.push(item.runId);
      } else if (item.runStatus === "failed") {
        items.push({
          kind: "assistant",
          id: item.runId,
          state: {
            ...initialAssistantState(item.runId),
            stage: "failed",
            errorCode: item.errorCode || "unknown_error",
          },
        });
      }
      continue;
    }

    if (item.role === "assistant") {
      items.push({
        kind: "assistant",
        id: item.runId ?? item.id,
        state: completedAssistant(item),
      });
      continue;
    }

    items.push({ kind: "notice", id: item.id, content: item.content });
  }

  return {
    items,
    activeRunIds,
    nextBeforeMessageId: page.nextBeforeMessageId,
  };
}
