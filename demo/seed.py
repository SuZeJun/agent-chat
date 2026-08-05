"""幂等初始化 Agent Chat 演示知识库和 FAQ。"""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path
from typing import Any


API_BASE_URL = os.getenv("API_BASE_URL", "http://127.0.0.1:8080").rstrip("/")
ADMIN_ID = os.getenv("DEMO_ADMIN_ID", "demo-admin").strip()
KNOWLEDGE_BASE_NAME = os.getenv(
    "DEMO_KNOWLEDGE_BASE_NAME", "Agent Chat SaaS 演示知识库"
).strip()
FAQ_PATH = Path(
    os.getenv("DEMO_FAQ_PATH", str(Path(__file__).with_name("saas-api-faq.csv")))
)


class SeedError(RuntimeError):
    """表示演示数据初始化无法安全完成。"""


def request(
    method: str,
    path: str,
    *,
    body: bytes | None = None,
    content_type: str | None = None,
    timeout: float = 10,
) -> tuple[int, dict[str, Any]]:
    headers = {"Accept": "application/json", "X-Admin-ID": ADMIN_ID}
    if content_type:
        headers["Content-Type"] = content_type
    outgoing = urllib.request.Request(
        API_BASE_URL + path,
        data=body,
        headers=headers,
        method=method,
    )
    try:
        with urllib.request.urlopen(outgoing, timeout=timeout) as response:
            payload = response.read()
            try:
                decoded = json.loads(payload) if payload else {}
            except json.JSONDecodeError as error:
                raise SeedError("demo API returned an invalid response") from error
            if not isinstance(decoded, dict):
                raise SeedError("demo API returned an invalid response")
            return response.status, decoded
    except urllib.error.HTTPError as error:
        payload = error.read()
        try:
            decoded = json.loads(payload) if payload else {}
        except json.JSONDecodeError:
            decoded = {}
        return error.code, decoded
    except (OSError, urllib.error.URLError) as error:
        raise SeedError("demo API is unavailable") from error


def wait_until_ready(attempts: int = 30, delay_seconds: float = 2) -> None:
    for _ in range(attempts):
        try:
            status, _ = request("GET", "/readyz", timeout=2)
            if status == 200:
                return
        except SeedError:
            pass
        time.sleep(delay_seconds)
    raise SeedError("demo API did not become ready")


def list_knowledge_bases() -> list[dict[str, Any]]:
    status, payload = request("GET", "/api/v1/admin/knowledge-bases")
    if status != 200 or not isinstance(payload.get("items"), list):
        raise SeedError("cannot list demo knowledge bases")
    return payload["items"]


def find_knowledge_base() -> str | None:
    matches = []
    for item in list_knowledge_bases():
        if item.get("name") != KNOWLEDGE_BASE_NAME or item.get("status") != "active":
            continue
        identifier = item.get("id")
        if isinstance(identifier, str) and identifier:
            matches.append(identifier)
    if len(matches) > 1:
        raise SeedError("demo knowledge base name is ambiguous")
    return matches[0] if matches else None


def ensure_knowledge_base() -> str:
    existing = find_knowledge_base()
    if existing:
        return existing
    body = json.dumps(
        {
            "name": KNOWLEDGE_BASE_NAME,
            "description": "用于演示 RAG、工具审批、Trace、Eval 与人工接管的 SaaS API 知识。",
        },
        ensure_ascii=False,
    ).encode("utf-8")
    status, payload = request(
        "POST",
        "/api/v1/admin/knowledge-bases",
        body=body,
        content_type="application/json; charset=utf-8",
    )
    if status == 409:
        existing = find_knowledge_base()
        if existing:
            return existing
    identifier = payload.get("id")
    if status != 201 or not isinstance(identifier, str) or not identifier:
        raise SeedError("cannot create demo knowledge base")
    return identifier


def multipart_file(field_name: str, path: Path) -> tuple[bytes, str]:
    if not path.is_file():
        raise SeedError(f"demo FAQ file is missing: {path.name}")
    boundary = "agent-chat-" + uuid.uuid4().hex
    header = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="{field_name}"; filename="{path.name}"\r\n'
        "Content-Type: text/csv; charset=utf-8\r\n\r\n"
    ).encode("utf-8")
    body = header + path.read_bytes() + f"\r\n--{boundary}--\r\n".encode("ascii")
    return body, f"multipart/form-data; boundary={boundary}"


def import_faq(knowledge_base_id: str) -> dict[str, Any]:
    body, content_type = multipart_file("file", FAQ_PATH)
    status, payload = request(
        "POST",
        f"/api/v1/admin/knowledge-bases/{knowledge_base_id}/faq-imports",
        body=body,
        content_type=content_type,
        timeout=30,
    )
    if status not in (200, 202):
        raise SeedError("cannot import demo FAQ")
    return payload


def main() -> int:
    try:
        wait_until_ready()
        knowledge_base_id = ensure_knowledge_base()
        imported = import_faq(knowledge_base_id)
    except SeedError as error:
        print(f"demo seed failed: {error}", file=sys.stderr)
        return 1
    print(
        json.dumps(
            {
                "knowledgeBaseId": knowledge_base_id,
                "knowledgeBaseName": KNOWLEDGE_BASE_NAME,
                "faqImportId": imported.get("id"),
                "duplicate": imported.get("duplicate", False),
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
