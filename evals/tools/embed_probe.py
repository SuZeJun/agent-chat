"""测量 FAQ 文档侧表示对真实查询的余弦分离度，用于标定 Answerability 阈值。

这是**手动运行**的工具，会调用付费 embedding Provider，因此不放在 `evals/runner`
下、也不会被 pytest 采集。CI 永远不应执行它。

何时需要运行：

- 知识库语料规模或领域发生明显变化
- 更换 embedding Provider 或模型
- 怀疑 Answerability 三分支的判定过于激进或过于保守

如何读结果：脚本对每种文档侧表示，统计「确实可回答」查询的最低分与「确实不可
回答」查询的最高分，两者之差即分离度。分离度越大，单一 top1 分数越能可靠区分
两类问题；分离度为负说明该表示方式下两簇重叠，阈值无论如何取值都必然误判。

安全边界（answerable 阈值）应当与「不可回答的最高分」保持余量，而不是塞进两簇
之间的窄缝，否则换一批问题就会失效。详见 docs/EVALUATION.md。

用法：

    python evals/tools/embed_probe.py
    python evals/tools/embed_probe.py --corpus evals/tools/probe_corpus.json
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Callable

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_CORPUS = ROOT / "evals" / "tools" / "probe_corpus.json"
EMBEDDING_BATCH_SIZE = 64
REQUEST_TIMEOUT_SECONDS = 60

# 文档侧表示。q_and_a 必须与 knowledgeindex.DeterministicChunker 的 FAQ 切片格式
# 保持一致，否则测得的分数无法代表生产检索行为。
VARIANTS: dict[str, Callable[[str, str], str]] = {
    "question_only": lambda question, _answer: question,
    "q_and_a": lambda question, answer: f"问题：{question}\n答案：{answer}",
    "answer_only": lambda _question, answer: answer,
}


class ProbeError(RuntimeError):
    """工具自身可预期的失败，不需要向用户暴露堆栈。"""


def read_env_value(key: str) -> str | None:
    env_path = ROOT / ".env"
    if not env_path.exists():
        return None
    pattern = re.compile(rf"^\s*{re.escape(key)}\s*=\s*(.*)$")
    for line in env_path.read_text(encoding="utf-8").splitlines():
        match = pattern.match(line)
        if match:
            return match.group(1).strip()
    return None


def resolve_setting(key: str, fallback: str | None = None) -> str:
    value = os.environ.get(key) or read_env_value(key) or fallback
    if not value:
        raise ProbeError(f"{key} 未配置：请在环境变量或 .env 中提供")
    return value


def load_corpus(path: Path) -> tuple[list[dict], list[dict]]:
    if not path.exists():
        raise ProbeError(f"语料文件不存在：{path}")
    corpus = json.loads(path.read_text(encoding="utf-8"))
    faqs = corpus.get("faqs") or []
    queries = corpus.get("queries") or []
    if not faqs or not queries:
        raise ProbeError("语料必须同时包含非空的 faqs 和 queries")
    if not any(item.get("answerable") for item in queries):
        raise ProbeError("语料缺少 answerable 为 true 的查询，无法计算分离度")
    if not any(not item.get("answerable") for item in queries):
        raise ProbeError("语料缺少 answerable 为 false 的查询，无法计算分离度")
    return faqs, queries


def embed(texts: list[str], endpoint: str, model: str, dimensions: int, api_key: str) -> list[list[float]]:
    vectors: list[list[float]] = []
    for start in range(0, len(texts), EMBEDDING_BATCH_SIZE):
        batch = texts[start : start + EMBEDDING_BATCH_SIZE]
        payload = json.dumps(
            {"model": model, "input": batch, "dimensions": dimensions}
        ).encode("utf-8")
        request = urllib.request.Request(
            endpoint,
            data=payload,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_SECONDS) as response:
                body = json.loads(response.read().decode("utf-8"))
        except urllib.error.HTTPError as error:
            raise ProbeError(f"embedding 请求失败：HTTP {error.code}") from None
        except urllib.error.URLError as error:
            raise ProbeError(f"embedding 请求无法送达：{error.reason}") from None

        items = sorted(body.get("data", []), key=lambda item: item["index"])
        if len(items) != len(batch):
            raise ProbeError("embedding 返回数量与输入不一致")
        vectors.extend(item["embedding"] for item in items)
    return vectors


def cosine(left: list[float], right: list[float]) -> float:
    dot = sum(a * b for a, b in zip(left, right))
    left_norm = math.sqrt(sum(a * a for a in left))
    right_norm = math.sqrt(sum(b * b for b in right))
    if left_norm == 0 or right_norm == 0:
        return 0.0
    return dot / (left_norm * right_norm)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--corpus", type=Path, default=DEFAULT_CORPUS, help="语料文件路径")
    arguments = parser.parse_args()

    try:
        faqs, queries = load_corpus(arguments.corpus)
        api_key = resolve_setting("EMBEDDING_API_KEY")
        base_url = resolve_setting(
            "EMBEDDING_BASE_URL", "https://open.bigmodel.cn/api/paas/v4"
        )
        model = resolve_setting("EMBEDDING_MODEL", "embedding-3")
        dimensions = int(resolve_setting("EMBEDDING_DIM", "1024"))
        endpoint = base_url.rstrip("/") + "/embeddings"

        print(f"语料：{arguments.corpus}（{len(faqs)} 条 FAQ，{len(queries)} 个查询）")
        print(f"模型：{model}（{dimensions} 维）\n")

        query_vectors = embed(
            [item["query"] for item in queries], endpoint, model, dimensions, api_key
        )

        summaries: dict[str, tuple[float, float, float]] = {}
        details: dict[str, list[tuple[float, bool, str, str]]] = {}
        for name, build in VARIANTS.items():
            document_vectors = embed(
                [build(faq["question"], faq["answer"]) for faq in faqs],
                endpoint,
                model,
                dimensions,
                api_key,
            )
            rows = []
            for item, query_vector in zip(queries, query_vectors):
                scored = sorted(
                    (
                        (cosine(query_vector, document_vector), faqs[index]["question"])
                        for index, document_vector in enumerate(document_vectors)
                    ),
                    reverse=True,
                )
                top_score, top_question = scored[0]
                rows.append((top_score, bool(item["answerable"]), item["query"], top_question))
            details[name] = rows

            answerable = [row[0] for row in rows if row[1]]
            unanswerable = [row[0] for row in rows if not row[1]]
            low, high = min(answerable), max(unanswerable)
            summaries[name] = (low, high, low - high)

        print(f"{'表示方式':<16}{'可回答最低':>12}{'不可回答最高':>14}{'分离度':>10}")
        print("-" * 54)
        for name, (low, high, gap) in summaries.items():
            print(f"{name:<16}{low:>12.4f}{high:>14.4f}{gap:>10.4f}")

        for name, rows in details.items():
            print(f"\n=== {name} ===")
            for score, answerable, query, top_question in sorted(rows, reverse=True):
                flag = "可回答  " if answerable else "不可回答"
                print(f"  {score:.4f}  {flag}  {query}  ->  {top_question}")

        best_name, best = max(summaries.items(), key=lambda item: item[1][2])
        print(f"\n分离度最大：{best_name}（gap={best[2]:.4f}）")
        if best[2] <= 0:
            print("警告：所有表示方式下两簇均重叠，单一 top1 分数无法可靠区分，")
            print("      应考虑 rerank 或多证据聚合，而不是继续调整阈值。")
        else:
            print(f"提示：answerable 阈值应高于 {best[1]:.4f} 并保留余量，而非贴近该值。")
    except ProbeError as error:
        print(f"错误：{error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
