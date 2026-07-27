import type { Citation } from "@/lib/types";

/**
 * FAQ 切片以「问题：X\n答案：Y」的形式存储，该前缀只是 embedding 的输入脚手架。
 * 标题已经展示了问题，正文再重复一遍既冗余又暴露内部格式，因此只展示答案部分；
 * 无法匹配时（例如 Markdown 切片或超长 FAQ 的后续切片）原样展示。
 */
const FAQ_SCAFFOLD_PATTERN = /^问题：[\s\S]*?\n答案：/;

function displayExcerpt(excerpt: string): string {
  return excerpt.replace(FAQ_SCAFFOLD_PATTERN, "").trim() || excerpt.trim();
}

/**
 * 回答引用。
 *
 * 引用不做成折叠项：可核对的来源是这个产品的核心承诺，excerpt 展示的是
 * 实际进入模型上下文的切片原文，允许用户直接比对回答与依据。
 */
export function CitationList({ citations }: { citations: Citation[] }) {
  if (citations.length === 0) {
    return null;
  }

  return (
    <div className="mt-3 border-t border-border pt-3">
      <p className="mb-2 text-xs font-medium text-muted-foreground">引用来源</p>
      <ul className="space-y-2">
        {citations.map((citation) => (
          <li key={citation.sourceId || citation.chunkId} className="text-xs">
            <div className="flex items-baseline gap-2">
              {/* 标号必须与回答正文中的来源标记完全一致，用户才能逐条核对。 */}
              <span className="shrink-0 font-mono text-muted-foreground">
                [{citation.sourceId}]
              </span>
              <span className="font-medium">{citation.title}</span>
              <span
                className="ml-auto shrink-0 font-mono text-muted-foreground"
                title="向量相似度"
              >
                {citation.score.toFixed(4)}
              </span>
            </div>
            <p className="mt-1 pl-8 whitespace-pre-wrap text-muted-foreground">
              {displayExcerpt(citation.excerpt)}
            </p>
          </li>
        ))}
      </ul>
    </div>
  );
}
