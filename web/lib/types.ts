/** 与 Go 后端 internal/domain/chat 保持一致的运行事件契约。 */

export type RunEventType =
  | "run.started"
  | "run.status"
  | "retrieval.completed"
  | "answerability.decided"
  | "message.delta"
  | "message.citation"
  | "run.completed"
  | "run.failed";

export type Decision = "answerable" | "needs_clarification" | "unanswerable";

export type NextAction = "provide_details" | "request_human_support";

/** 检索证据。进入 Answerability Gate 但未必进入回答引用。 */
export type Evidence = {
  sourceId: string;
  title: string;
  score: number;
  rank: number;
  chunkId: string;
  documentId: string;
  versionId: string;
  documentType: string;
};

/** 回答引用。excerpt 是实际进入模型上下文的切片原文，可供人工核对。 */
export type Citation = Evidence & { excerpt: string };

export type RunEvent = {
  eventId: string;
  runId: string;
  sequence: number;
  type: RunEventType;
  payload: Record<string, unknown>;
  createdAt: string;
};

export type CreateConversationResponse = {
  id: string;
  knowledgeBaseId: string;
  status: string;
};

export type SendMessageResponse = {
  messageId: string;
  runId: string;
  runStatus: string;
  duplicate: boolean;
};

export type ApiErrorBody = {
  error: { code: string; message: string };
  requestId: string;
};

/** Run 在前端的可见阶段，用于展示处理进度。 */
export type RunStage =
  | "pending"
  | "retrieving"
  | "deciding"
  | "generating"
  | "completed"
  | "failed";

export type AssistantState = {
  runId: string;
  stage: RunStage;
  answer: string;
  decision?: Decision;
  reason?: string;
  confidence?: number;
  nextAction?: NextAction;
  evidence: Evidence[];
  citations: Citation[];
  errorCode?: string;
};
