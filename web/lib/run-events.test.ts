import { describe, expect, it } from "vitest";

import { initialAssistantState, isTerminalEvent, reduceRunEvent } from "@/lib/run-events";
import type { AssistantState, RunEvent, RunEventType } from "@/lib/types";

function event(
  type: RunEventType,
  payload: Record<string, unknown>,
  sequence = 1,
): RunEvent {
  return {
    eventId: `evt_${sequence}`,
    runId: "run_1",
    sequence,
    type,
    payload,
    createdAt: "2026-07-27T00:00:00Z",
  };
}

function reduceAll(events: RunEvent[]): AssistantState {
  return events.reduce(reduceRunEvent, initialAssistantState("run_1"));
}

describe("reduceRunEvent", () => {
  it("累积增量而非覆盖", () => {
    const state = reduceAll([
      event("message.delta", { delta: "重置密码" }),
      event("message.delta", { delta: "的步骤如下：" }, 2),
      event("message.delta", { delta: "登录后进入设置页。" }, 3),
    ]);

    expect(state.answer).toBe("重置密码的步骤如下：登录后进入设置页。");
  });

  // 失败的尝试可能已经发出过增量。若不在新一次尝试开始时重置，
  // 重试产生的回答会接在上一次的残段后面，用户看到的是两段拼接的乱码。
  it("新一次尝试开始时清空上一次的残留", () => {
    const state = reduceAll([
      event("run.started", { attempt: 1 }),
      event("retrieval.completed", { evidence: [{ sourceId: "S1", score: 0.9 }] }, 2),
      event("message.delta", { delta: "这是失败尝试的半截回答" }, 3),
      event("run.status", { status: "running", retrying: true }, 4),
      event("run.started", { attempt: 2 }, 5),
      event("message.delta", { delta: "这是重试后的完整回答" }, 6),
    ]);

    expect(state.answer).toBe("这是重试后的完整回答");
    expect(state.evidence).toHaveLength(0);
    expect(state.stage).toBe("retrieving");
  });

  it("按顺序推进处理阶段", () => {
    const stages = [
      event("run.started", { attempt: 1 }),
      event("retrieval.completed", { evidence: [] }, 2),
      event("answerability.decided", { decision: "answerable", confidence: 0.8 }, 3),
    ].reduce<AssistantState[]>((states, current) => {
      const previous = states.at(-1) ?? initialAssistantState("run_1");
      return [...states, reduceRunEvent(previous, current)];
    }, []);

    expect(stages.map((state) => state.stage)).toEqual([
      "retrieving",
      "deciding",
      "generating",
    ]);
  });

  it("按 sourceId 去重引用", () => {
    const citation = { sourceId: "S1", title: "如何重置密码？", excerpt: "答案", score: 0.76 };
    const state = reduceAll([
      event("message.citation", { citation }),
      event("message.citation", { citation }, 2),
    ]);

    expect(state.citations).toHaveLength(1);
    expect(state.citations[0].sourceId).toBe("S1");
  });

  it("从持久化事件恢复结构化工单草稿", () => {
    const state = reduceAll([
      event("approval.required", {
        approvalId: "approval_1",
        draft: {
          title: "无法导出账单",
          description: "点击导出后没有响应。",
          priority: "high",
        },
        expiresAt: "2026-08-04T01:00:00Z",
      }),
      event("run.completed", { nextAction: "confirm_ticket" }, 2),
    ]);

    expect(state.stage).toBe("completed");
    expect(state.nextAction).toBe("confirm_ticket");
    expect(state.approval).toEqual({
      approvalId: "approval_1",
      draft: {
        title: "无法导出账单",
        description: "点击导出后没有响应。",
        priority: "high",
      },
      expiresAt: "2026-08-04T01:00:00Z",
    });
  });

  it("记录失败错误码并停在失败阶段", () => {
    const state = reduceAll([
      event("run.started", { attempt: 1 }),
      event("run.failed", { errorCode: "rag_generation_failed" }, 2),
    ]);

    expect(state.stage).toBe("failed");
    expect(state.errorCode).toBe("rag_generation_failed");
  });
});

describe("isTerminalEvent", () => {
  it("只有终态事件才允许关闭事件流", () => {
    expect(isTerminalEvent("run.completed")).toBe(true);
    expect(isTerminalEvent("run.failed")).toBe(true);
    expect(isTerminalEvent("message.delta")).toBe(false);
    expect(isTerminalEvent("run.started")).toBe(false);
  });
});
