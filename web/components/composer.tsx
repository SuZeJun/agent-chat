"use client";

import { SendHorizontal } from "lucide-react";
import { useState, type FormEvent, type KeyboardEvent } from "react";

import { Button } from "@/components/ui/button";

export function Composer({
  disabled,
  onSend,
}: {
  disabled: boolean;
  onSend: (content: string) => void;
}) {
  const [draft, setDraft] = useState("");

  const submit = () => {
    const content = draft.trim();
    if (!content || disabled) {
      return;
    }
    setDraft("");
    onSend(content);
  };

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    submit();
  };

  // Enter 发送、Shift+Enter 换行，符合聊天输入的普遍预期。
  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      submit();
    }
  };

  return (
    <form
      onSubmit={handleSubmit}
      className="flex items-end gap-2 border-t border-border bg-background p-3"
    >
      <textarea
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={handleKeyDown}
        rows={1}
        placeholder="输入问题…"
        aria-label="输入问题"
        className="max-h-32 min-h-9 flex-1 resize-y rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
      />
      <Button type="submit" size="lg" disabled={disabled || !draft.trim()}>
        <SendHorizontal aria-hidden />
        发送
      </Button>
    </form>
  );
}
