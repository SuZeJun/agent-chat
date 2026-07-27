/**
 * 服务端配置。
 *
 * 演示身份和知识库绑定只存在于服务端：浏览器只与 Next.js 路由处理器通信，
 * 由后者转发到 Go API 并注入身份头，因此后端无需开启 CORS，身份也不会
 * 暴露给客户端代码。
 */

export type ServerConfig = {
  apiBaseUrl: string;
  customerId: string;
  adminId: string;
  knowledgeBaseId: string;
  knowledgeBaseName: string;
};

function requireEnv(key: string): string {
  const value = process.env[key]?.trim();
  if (!value) {
    throw new Error(`缺少环境变量 ${key}，请参考 web/.env.example 配置`);
  }
  return value;
}

export function readServerConfig(): ServerConfig {
  return {
    apiBaseUrl: (process.env.API_BASE_URL?.trim() || "http://127.0.0.1:8080").replace(
      /\/+$/,
      "",
    ),
    customerId: process.env.DEMO_CUSTOMER_ID?.trim() || "demo-customer",
    // Run 详情面向管理员与 AI 运营人员，与客户身份分开，避免内部 Trace
    // 经由客户作用域的路由泄露。
    adminId: process.env.DEMO_ADMIN_ID?.trim() || "demo-admin",
    // 当前后端没有「列出知识库」接口，因此知识库由配置绑定而非页面选择。
    knowledgeBaseId: requireEnv("KNOWLEDGE_BASE_ID"),
    knowledgeBaseName: process.env.KNOWLEDGE_BASE_NAME?.trim() || "演示知识库",
  };
}
