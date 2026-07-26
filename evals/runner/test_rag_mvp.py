import json
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def test_rag_mvp_safety_gate(tmp_path: Path) -> None:
    json_report = tmp_path / "rag_mvp.json"
    markdown_report = tmp_path / "rag_mvp.md"
    completed = subprocess.run(
        [
            "go",
            "run",
            "./cmd/rag-eval",
            "--cases",
            "evals/cases/rag_mvp.json",
            "--json",
            str(json_report),
            "--markdown",
            str(markdown_report),
        ],
        cwd=ROOT,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )

    assert completed.returncode == 0, completed.stdout + completed.stderr
    report = json.loads(json_report.read_text(encoding="utf-8"))
    assert report["total"] >= 10
    assert report["passed"] == report["total"]
    assert report["failed"] == 0
    assert markdown_report.read_text(encoding="utf-8").startswith("# RAG MVP Eval")
