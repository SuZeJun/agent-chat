import json
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
CASES = ROOT / "evals" / "cases" / "mvp.json"
BASELINE = ROOT / "evals" / "baselines" / "mvp.json"


def run_eval(
    tmp_path: Path, cases: Path = CASES, baseline: Path = BASELINE
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [
            "go",
            "run",
            "./cmd/rag-eval",
            "--cases",
            str(cases),
            "--baseline",
            str(baseline),
            "--json",
            str(tmp_path / "mvp.json"),
            "--markdown",
            str(tmp_path / "mvp.md"),
        ],
        cwd=ROOT,
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )


def test_mvp_release_gate(tmp_path: Path) -> None:
    completed = run_eval(tmp_path)

    assert completed.returncode == 0, completed.stdout + completed.stderr
    report = json.loads((tmp_path / "mvp.json").read_text(encoding="utf-8"))
    assert report["total"] == 60
    assert report["passed"] == report["total"]
    assert report["failed"] == 0
    assert report["categoryCounts"] == {
        "answerable": 15,
        "handoff": 5,
        "multi_document": 8,
        "needs_clarification": 8,
        "prompt_injection": 4,
        "subscription_tool": 6,
        "ticket_approval": 6,
        "unanswerable": 8,
    }
    assert len(report["gates"]) == 8
    assert all(gate["passed"] for gate in report["gates"])
    assert report["metrics"]["recallAt5"] >= 0.85
    assert report["metrics"]["answerabilityMacroF1"] >= 0.8
    assert report["metrics"]["toolSelectionAccuracy"] >= 0.9
    assert (tmp_path / "mvp.md").read_text(encoding="utf-8").startswith(
        "# MVP Eval Report"
    )


def test_failed_case_returns_nonzero_and_is_locatable(tmp_path: Path) -> None:
    dataset = json.loads(CASES.read_text(encoding="utf-8"))
    dataset["cases"][0]["expected"]["decision"] = "unanswerable"
    broken_cases = tmp_path / "broken-mvp.json"
    broken_cases.write_text(json.dumps(dataset, ensure_ascii=False), encoding="utf-8")

    completed = run_eval(tmp_path, broken_cases)

    assert completed.returncode == 1, completed.stdout + completed.stderr
    report = json.loads((tmp_path / "mvp.json").read_text(encoding="utf-8"))
    failed = [result for result in report["results"] if not result["passed"]]
    assert failed[0]["locator"] == "case:answerable-01"
    assert "decision_mismatch" in failed[0]["failures"]


def test_release_thresholds_cannot_be_weakened(tmp_path: Path) -> None:
    dataset = json.loads(CASES.read_text(encoding="utf-8"))
    dataset["thresholds"]["approvalSafety"] = 0.99
    weakened_cases = tmp_path / "weakened-mvp.json"
    weakened_cases.write_text(json.dumps(dataset, ensure_ascii=False), encoding="utf-8")

    completed = run_eval(tmp_path, weakened_cases)

    # `go run` 将目标程序的非零退出码包装为自身退出码 1，并在 stderr 保留原始状态。
    assert completed.returncode != 0
    assert "may not weaken" in completed.stderr


def test_stale_baseline_fails_regression_gate(tmp_path: Path) -> None:
    baseline = json.loads(BASELINE.read_text(encoding="utf-8"))
    baseline["datasetVersion"] = "stale-version"
    stale_baseline = tmp_path / "stale-baseline.json"
    stale_baseline.write_text(json.dumps(baseline), encoding="utf-8")

    completed = run_eval(tmp_path, baseline=stale_baseline)

    assert completed.returncode != 0
    report = json.loads((tmp_path / "mvp.json").read_text(encoding="utf-8"))
    regression_gate = next(
        gate for gate in report["gates"] if gate["name"] == "baseline_regression"
    )
    assert regression_gate["passed"] is False
    assert "does not match" in regression_gate["detail"]
