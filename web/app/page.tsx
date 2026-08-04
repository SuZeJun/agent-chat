import { ChatPanel } from "@/components/chat-panel";
import { readServerConfig } from "@/lib/config";

export const dynamic = "force-dynamic";

export default function ChatPage() {
  // 客户聊天由服务端配置绑定知识库；管理员列表不能改变客户资源作用域。
  const { knowledgeBaseId, knowledgeBaseName } = readServerConfig();
  return (
    <ChatPanel
      knowledgeBaseId={knowledgeBaseId}
      knowledgeBaseName={knowledgeBaseName}
    />
  );
}
