import type {
  AssistantState,
  Citation,
  Decision,
  Evidence,
  NextAction,
  RunEvent,
  TicketDraft,
} from "@/lib/types";

export function initialAssistantState(runId: string): AssistantState {
  return { runId, stage: "pending", answer: "", evidence: [], citations: [] };
}

/** 终态事件之后不应再有事件到达，客户端据此关闭 EventSource。 */
export function isTerminalEvent(type: RunEvent["type"]): boolean {
  return type === "run.completed" || type === "run.failed";
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
}

function toEvidence(value: unknown): Evidence[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((item) => {
    const record = asRecord(item);
    return {
      sourceId: String(record.sourceId ?? ""),
      title: String(record.title ?? ""),
      score: Number(record.score ?? 0),
      rank: Number(record.rank ?? 0),
      chunkId: String(record.chunkId ?? ""),
      documentId: String(record.documentId ?? ""),
      versionId: String(record.versionId ?? ""),
      documentType: String(record.documentType ?? ""),
    };
  });
}

function toTicketDraft(value: unknown): TicketDraft | undefined {
  const record = asRecord(value);
  const priority = String(record.priority ?? "");
  if (
    typeof record.title !== "string" ||
    typeof record.description !== "string" ||
    !["low", "normal", "high"].includes(priority)
  ) {
    return undefined;
  }
  return {
    title: record.title,
    description: record.description,
    priority: priority as TicketDraft["priority"],
  };
}

/**
 * 将单个事件归约进回答状态。
 *
 * message.delta 采用追加而非覆盖：当前后端一次性发送完整回答，接入流式后
 * 同一 Run 会收到多条增量，此处无需再改。
 */
export function reduceRunEvent(
  state: AssistantState,
  event: RunEvent,
): AssistantState {
  const payload = asRecord(event.payload);

  switch (event.type) {
    // 每次尝试都以 run.started 开始，据此清空上一次尝试已累积的内容。
    // 失败的尝试可能已经发出过增量，若不重置，重试产生的回答会接在残段后面。
    case "run.started":
      return {
        ...initialAssistantState(state.runId),
        stage: "retrieving",
      };

    case "retrieval.completed":
      return { ...state, stage: "deciding", evidence: toEvidence(payload.evidence) };

    case "answerability.decided":
      return {
        ...state,
        stage: "generating",
        decision: payload.decision as Decision,
        reason: typeof payload.reason === "string" ? payload.reason : undefined,
        confidence:
          typeof payload.confidence === "number" ? payload.confidence : undefined,
        evidence: toEvidence(payload.evidence),
      };

    case "message.delta":
      return { ...state, answer: state.answer + String(payload.delta ?? "") };

    case "message.citation": {
      const record = asRecord(payload.citation);
      const citation: Citation = {
        ...toEvidence([record])[0],
        excerpt: String(record.excerpt ?? ""),
      };
      // 后端保证按顺序追加，重复出现同一来源时以先到者为准。
      if (state.citations.some((item) => item.sourceId === citation.sourceId)) {
        return state;
      }
      return { ...state, citations: [...state.citations, citation] };
    }

    case "approval.required": {
      const approvalId = String(payload.approvalId ?? "");
      const draft = toTicketDraft(payload.draft);
      if (!approvalId || !draft) {
        return state;
      }
      return {
        ...state,
        approval: {
          approvalId,
          draft,
          expiresAt:
            typeof payload.expiresAt === "string" ? payload.expiresAt : undefined,
        },
      };
    }

    case "run.completed":
      return {
        ...state,
        stage: "completed",
        nextAction:
          typeof payload.nextAction === "string" && payload.nextAction
            ? (payload.nextAction as NextAction)
            : undefined,
      };

    case "run.failed":
      return {
        ...state,
        stage: "failed",
        errorCode:
          typeof payload.errorCode === "string" ? payload.errorCode : "unknown_error",
      };

    // run.status 只是状态快照，不改变已归约的内容。
    default:
      return state;
  }
}
