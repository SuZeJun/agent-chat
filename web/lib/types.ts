/** 与 Go 后端 internal/domain/chat 保持一致的运行事件契约。 */

export type RunEventType =
  | "run.started"
  | "run.status"
  | "retrieval.completed"
  | "answerability.decided"
  | "message.delta"
  | "message.citation"
  | "approval.required"
  | "approval.confirmed"
  | "approval.cancelled"
  | "approval.expired"
  | "ticket.created"
  | "run.completed"
  | "run.failed";

export type Decision = "answerable" | "needs_clarification" | "unanswerable";

export type NextAction =
  | "provide_details"
  | "request_human_support"
  | "confirm_ticket";

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

export type KnowledgeBase = {
  id: string;
  name: string;
  description: string;
  status: "active" | "disabled";
};

export type KnowledgeBaseListResponse = {
  items: KnowledgeBase[];
};

export type FAQImportResult = {
  id: string;
  status: FAQIndexStatus;
  totalRows: number;
  readyRows: number;
  failedRows: number;
  duplicate?: boolean;
};

export type FAQIndexStatus = "pending" | "indexing" | "ready" | "failed";

export type FAQImportItem = {
  rowNumber: number;
  documentId: string;
  versionId: string;
  status: FAQIndexStatus;
  errorCode?: string;
};

export type FAQImportStatus = FAQImportResult & {
  sourceName: string;
  items: FAQImportItem[];
  createdAt: string;
};

export type MarkdownVersion = {
  id: string;
  number: number;
  status: FAQIndexStatus;
  errorCode?: string;
  active: boolean;
  createdAt: string;
  indexedAt?: string;
};

export type MarkdownDocument = {
  id: string;
  knowledgeBaseId: string;
  title: string;
  sourceUrl?: string;
  activeVersionId?: string;
  latestVersion: number;
  latestContent?: string;
  versions: MarkdownVersion[];
  createdAt: string;
  updatedAt: string;
};

export type MarkdownDocumentListResponse = { items: MarkdownDocument[] };

export type TicketDraft = {
  title: string;
  description: string;
  priority: "low" | "normal" | "high";
};

export type TicketApproval = {
  approvalId: string;
  status: "pending" | "approved" | "cancelled" | "expired";
  draft: TicketDraft;
  ticket?: { id: string; number: string };
  executionStatus:
    | "awaiting_confirmation"
    | "pending"
    | "cancelled"
    | "expired"
    | "succeeded";
};

export type TicketApprovalPrompt = {
  approvalId: string;
  draft: TicketDraft;
  expiresAt?: string;
};

export type ToolCall = {
  name: string;
  status: string;
  errorCode?: string;
  durationMillis: number;
};

/** 管理员可见的 Run 执行步骤。 */
export type RunTraceStep = {
  order: number;
  name: string;
  component: string;
  componentType: string;
  status: string;
  durationMillis: number;
  promptTokens: number;
  completionTokens: number;
  startedAt: string;
  completedAt: string;
};

/** Run 结果快照，字段与 Graph Output 的持久化形式一致。 */
export type RunResult = {
  answer?: string;
  nextAction?: NextAction;
  nodePath?: string[];
  citations?: Citation[];
  toolCalls?: ToolCall[];
  ticketDraft?: TicketDraft;
  approvalId?: string;
  approvalExpiresAt?: string;
  assessment?: {
    decision?: Decision;
    reason?: string;
    confidence?: number;
    evidence?: Evidence[];
  };
};

export type MessageHistoryItem = {
  id: string;
  role: "customer" | "assistant" | "agent" | "system";
  content: string;
  runId?: string;
  runStatus?: "pending" | "running" | "completed" | "failed";
  result?: RunResult;
  errorCode?: string;
  createdAt: string;
};

export type MessageHistoryResponse = {
  items: MessageHistoryItem[];
  nextBeforeMessageId?: string;
};

export type RunTrace = {
  runId: string;
  requestId: string;
  conversationId: string;
  question: string;
  status: string;
  errorCode?: string;
  result: RunResult;
  steps: RunTraceStep[];
  events: RunEvent[];
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
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
  approval?: TicketApprovalPrompt;
  errorCode?: string;
};
