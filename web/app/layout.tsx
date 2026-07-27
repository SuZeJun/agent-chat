import type { Metadata } from "next";

import "./globals.css";

export const metadata: Metadata = {
  title: "Agent Chat",
  description: "带可核对引用的企业知识问答",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="zh-CN">
      <body className="antialiased">{children}</body>
    </html>
  );
}
