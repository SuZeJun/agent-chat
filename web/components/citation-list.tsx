import type { Citation } from "@/lib/types";

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
        {citations.map((citation, index) => (
          <li key={citation.sourceId || citation.chunkId} className="text-xs">
            <div className="flex items-baseline gap-2">
              <span className="shrink-0 font-mono text-muted-foreground">
                [{index + 1}]
              </span>
              <span className="font-medium">{citation.title}</span>
              <span
                className="ml-auto shrink-0 font-mono text-muted-foreground"
                title="向量相似度"
              >
                {citation.score.toFixed(4)}
              </span>
            </div>
            <p className="mt-1 pl-7 whitespace-pre-wrap text-muted-foreground">
              {citation.excerpt}
            </p>
          </li>
        ))}
      </ul>
    </div>
  );
}
