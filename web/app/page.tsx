import Link from "next/link";

import { ChatPanel } from "@/components/chat-panel";
import { readServerConfig } from "@/lib/config";
import { resolveDemoKnowledgeBase } from "@/lib/demo-knowledge-base-server";

export const dynamic = "force-dynamic";

export default async function ChatPage() {
  const config = readServerConfig();
  try {
    // 客户聊天作用域只由服务端配置或 seed 的唯一名称决定，浏览器不能覆盖。
    const knowledgeBase = await resolveDemoKnowledgeBase(config);
    return (
      <ChatPanel
        knowledgeBaseId={knowledgeBase.id}
        knowledgeBaseName={knowledgeBase.name}
      />
    );
  } catch {
    return (
      <main className="mx-auto flex min-h-dvh max-w-xl flex-col justify-center gap-4 px-6">
        <p className="text-sm font-medium text-destructive">演示知识库尚未就绪</p>
        <h1 className="text-2xl font-semibold">无法安全确定客户聊天的知识库作用域</h1>
        <p className="text-sm leading-6 text-muted-foreground">
          请等待 Docker Compose 的 demo-seed 服务完成，或在服务端设置
          KNOWLEDGE_BASE_ID。系统不会在作用域不明确时创建会话。
        </p>
        <Link className="text-sm underline underline-offset-4" href="/admin/knowledge">
          打开知识管理页检查状态
        </Link>
      </main>
    );
  }
}
