"""对已启动的非模型 HTTP API 生成可复现的本地延迟报告。"""

from __future__ import annotations

import argparse
import json
import math
import os
import platform
import statistics
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


ENDPOINTS = (
    ("healthz", "/healthz", {}),
    ("readyz", "/readyz", {}),
    (
        "knowledge_bases",
        "/api/v1/admin/knowledge-bases",
        {"X-Admin-ID": os.getenv("DEMO_ADMIN_ID", "demo-admin").strip() or "demo-admin"},
    ),
)


def percentile(values: list[float], percentile_value: float) -> float:
    if not values:
        raise ValueError("cannot calculate percentile of an empty sample")
    ordered = sorted(values)
    index = max(0, math.ceil(percentile_value * len(ordered)) - 1)
    return ordered[index]


def measure(url: str, headers: dict[str, str], timeout: float) -> tuple[int, float]:
    request = urllib.request.Request(url, headers=headers, method="GET")
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            response.read()
            status = response.status
    except urllib.error.HTTPError as error:
        error.read()
        status = error.code
    return status, (time.perf_counter() - started) * 1000


def summarize(samples: list[float]) -> dict[str, float]:
    return {
        "minMs": min(samples),
        "meanMs": statistics.fmean(samples),
        "p50Ms": percentile(samples, 0.50),
        "p95Ms": percentile(samples, 0.95),
        "p99Ms": percentile(samples, 0.99),
        "maxMs": max(samples),
    }


def run(
    base_url: str,
    requests: int,
    warmup: int,
    timeout: float,
    threshold_ms: float,
) -> dict[str, Any]:
    results: list[dict[str, Any]] = []
    for name, path, headers in ENDPOINTS:
        url = base_url.rstrip("/") + path
        for _ in range(warmup):
            status, _ = measure(url, headers, timeout)
            if status != 200:
                raise RuntimeError(f"warmup {name} returned HTTP {status}")
        samples: list[float] = []
        for _ in range(requests):
            status, duration_ms = measure(url, headers, timeout)
            if status != 200:
                raise RuntimeError(f"benchmark {name} returned HTTP {status}")
            samples.append(duration_ms)
        summary = summarize(samples)
        results.append(
            {
                "name": name,
                "method": "GET",
                "path": path,
                "samples": requests,
                **summary,
                "passed": summary["p95Ms"] < threshold_ms,
            }
        )
    return {
        "generatedAt": datetime.now(timezone.utc).isoformat(),
        "baseUrl": base_url,
        "environment": {
            "python": platform.python_version(),
            "platform": platform.platform(),
            "mode": "sequential-local",
        },
        "configuration": {
            "requestsPerEndpoint": requests,
            "warmupPerEndpoint": warmup,
            "timeoutSeconds": timeout,
            "p95ThresholdMs": threshold_ms,
        },
        "results": results,
        "passed": all(item["passed"] for item in results),
    }


def markdown(report: dict[str, Any]) -> str:
    lines = [
        "# MVP 本地非模型 API 性能报告",
        "",
        f"- 生成时间：`{report['generatedAt']}`",
        f"- 目标：`{report['baseUrl']}`",
        f"- 模式：`{report['environment']['mode']}`",
        f"- 每端点样本：{report['configuration']['requestsPerEndpoint']}",
        f"- P95 门槛：`< {report['configuration']['p95ThresholdMs']:.0f} ms`",
        "",
        "| Endpoint | Samples | Mean | P50 | P95 | P99 | Result |",
        "| --- | ---: | ---: | ---: | ---: | ---: | --- |",
    ]
    for item in report["results"]:
        result = "PASS" if item["passed"] else "FAIL"
        lines.append(
            f"| `{item['method']} {item['path']}` | {item['samples']} | "
            f"{item['meanMs']:.2f} ms | {item['p50Ms']:.2f} ms | "
            f"{item['p95Ms']:.2f} ms | {item['p99Ms']:.2f} ms | {result} |"
        )
    lines.extend(
        [
            "",
            "该报告只覆盖本地非模型 API，不把模型或网络 Provider 延迟混入 NFR-004。",
            "",
        ]
    )
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:8080")
    parser.add_argument("--requests", type=int, default=100)
    parser.add_argument("--warmup", type=int, default=10)
    parser.add_argument("--timeout", type=float, default=5)
    parser.add_argument("--threshold-ms", type=float, default=300)
    parser.add_argument("--json", default="docs/reports/performance.json")
    parser.add_argument("--markdown", default="docs/reports/performance.md")
    arguments = parser.parse_args()
    if arguments.requests <= 0 or arguments.warmup < 0 or arguments.threshold_ms <= 0:
        parser.error("requests and threshold must be positive; warmup must not be negative")

    try:
        report = run(
            arguments.base_url,
            arguments.requests,
            arguments.warmup,
            arguments.timeout,
            arguments.threshold_ms,
        )
    except (OSError, RuntimeError, urllib.error.URLError) as error:
        print(f"benchmark failed: {error}", file=sys.stderr)
        return 2

    json_path = Path(arguments.json)
    markdown_path = Path(arguments.markdown)
    json_path.parent.mkdir(parents=True, exist_ok=True)
    markdown_path.parent.mkdir(parents=True, exist_ok=True)
    json_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    markdown_path.write_text(markdown(report), encoding="utf-8")
    print(f"performance benchmark: {'PASS' if report['passed'] else 'FAIL'}")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
