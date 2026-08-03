import { ChatPanel } from "@/components/chat-panel";
import { readServerConfig } from "@/lib/config";

export const dynamic = "force-dynamic";

export default function ChatPage() {
  // 知识库由服务端配置绑定，页面只展示名称；后端暂无列出知识库的接口。
  const { knowledgeBaseId, knowledgeBaseName } = readServerConfig();
  return (
    <ChatPanel
      knowledgeBaseId={knowledgeBaseId}
      knowledgeBaseName={knowledgeBaseName}
    />
  );
}
