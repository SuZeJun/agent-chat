import Link from "next/link";

import { FAQAdminPanel } from "@/components/faq-admin-panel";

export default function KnowledgeAdminPage() {
  return (
    <main className="min-h-dvh bg-muted/20">
      <div className="border-b border-border bg-background px-4 py-2">
        <p className="mx-auto flex max-w-6xl items-center gap-2 text-xs text-muted-foreground">
          <span className="rounded-md border border-border px-1.5 py-0.5">管理员</span>
          FAQ、Markdown 与持久化索引任务
          <Link href="/" className="ml-auto underline underline-offset-4">
            返回聊天
          </Link>
        </p>
      </div>
      <FAQAdminPanel />
    </main>
  );
}
