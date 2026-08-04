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

export type AdminServerConfig = Pick<ServerConfig, "apiBaseUrl" | "adminId">;

function requireEnv(key: string): string {
  const value = process.env[key]?.trim();
  if (!value) {
    throw new Error(`缺少环境变量 ${key}，请参考 web/.env.example 配置`);
  }
  return value;
}

/** readAdminServerConfig 不依赖客户聊天的知识库绑定，可用于空环境知识管理。 */
export function readAdminServerConfig(): AdminServerConfig {
  return {
    apiBaseUrl: (process.env.API_BASE_URL?.trim() || "http://127.0.0.1:8080").replace(
      /\/+$/,
      "",
    ),
    adminId: process.env.DEMO_ADMIN_ID?.trim() || "demo-admin",
  };
}

export function readServerConfig(): ServerConfig {
  const admin = readAdminServerConfig();
  return {
    ...admin,
    customerId: process.env.DEMO_CUSTOMER_ID?.trim() || "demo-customer",
    // 客户聊天固定绑定服务端配置的知识库；管理员列表不会改变客户资源作用域。
    knowledgeBaseId: requireEnv("KNOWLEDGE_BASE_ID"),
    knowledgeBaseName: process.env.KNOWLEDGE_BASE_NAME?.trim() || "演示知识库",
  };
}
