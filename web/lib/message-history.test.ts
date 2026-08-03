import { describe, expect, it } from "vitest";

import { restoreMessageHistory } from "@/lib/message-history";

describe("restoreMessageHistory", () => {
  it("restores completed answerability and citations without starting a run", () => {
    const restored = restoreMessageHistory({
      items: [
        {
          id: "message_customer",
          role: "customer",
          content: "如何重置密码？",
          runId: "run_1",
          runStatus: "completed",
          createdAt: "2026-08-04T00:00:00Z",
        },
        {
          id: "message_assistant",
          role: "assistant",
          content: "请进入设置页。[S1]",
          runId: "run_1",
          runStatus: "completed",
          result: {
            assessment: {
              decision: "answerable",
              reason: "knowledge_support_sufficient",
              confidence: 0.82,
              evidence: [],
            },
            citations: [
              {
                sourceId: "S1",
                title: "密码重置",
                score: 0.82,
                rank: 1,
                chunkId: "chunk_1",
                documentId: "document_1",
                versionId: "version_1",
                documentType: "faq",
                excerpt: "进入设置页重置密码。",
              },
            ],
          },
          createdAt: "2026-08-04T00:00:01Z",
        },
      ],
    });

    expect(restored.activeRunId).toBeNull();
    expect(restored.items).toHaveLength(2);
    const assistant = restored.items[1];
    expect(assistant.kind).toBe("assistant");
    if (assistant.kind === "assistant") {
      expect(assistant.state.stage).toBe("completed");
      expect(assistant.state.decision).toBe("answerable");
      expect(assistant.state.citations[0].sourceId).toBe("S1");
    }
  });

  it("resumes the existing pending run instead of creating another one", () => {
    const restored = restoreMessageHistory({
      items: [
        {
          id: "message_customer",
          role: "customer",
          content: "帮我建个工单",
          runId: "run_pending",
          runStatus: "running",
          createdAt: "2026-08-04T00:00:00Z",
        },
      ],
      nextBeforeMessageId: "message_older",
    });

    expect(restored.activeRunId).toBe("run_pending");
    expect(restored.nextBeforeMessageId).toBe("message_older");
    expect(restored.items.map((item) => item.kind)).toEqual(["customer", "assistant"]);
  });

  it.each([
    ["needs_clarification", "provide_details"],
    ["unanswerable", "request_human_support"],
  ] as const)("restores the %s branch after refresh", (decision, nextAction) => {
    const restored = restoreMessageHistory({
      items: [
        {
          id: `assistant_${decision}`,
          role: "assistant",
          content: decision === "needs_clarification" ? "请补充错误提示。" : "知识库暂无相关信息。",
          runId: `run_${decision}`,
          runStatus: "completed",
          result: {
            assessment: { decision, reason: `reason_${decision}`, evidence: [] },
            nextAction,
          },
          createdAt: "2026-08-04T00:00:01Z",
        },
      ],
    });

    const assistant = restored.items[0];
    expect(assistant.kind).toBe("assistant");
    if (assistant.kind === "assistant") {
      expect(assistant.state.stage).toBe("completed");
      expect(assistant.state.decision).toBe(decision);
      expect(assistant.state.nextAction).toBe(nextAction);
    }
  });
});
