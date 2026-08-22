from __future__ import annotations

from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"


def test_ci_keeps_every_functional_release_gate() -> None:
    workflow = WORKFLOW.read_text(encoding="utf-8")
    required_jobs = {
        "backend": "go test ./...",
        "schema": "Apply all migrations in order",
        "api-contract": "Run core black-box contract",
        "sandbox-contract": "Run deterministic sandbox contract",
        "web": "pnpm test",
        "browser-e2e": "Run required browser gate",
        "release-regressions": "Verify lost-response admin reconciliation",
        "release-gate": "All required release checks passed",
    }
    for job, evidence in required_jobs.items():
        assert f"  {job}:" in workflow, f"release job {job!r} was removed"
        assert evidence in workflow, f"release evidence {evidence!r} was removed"

    assert "test_sandbox_states.py" in workflow
    assert 'REQUIRE_E2E_TARGETS: "1"' in workflow
    assert 'REQUIRE_SANDBOX_CONTRACT: "1"' in workflow


def test_release_gate_waits_for_functionality_not_only_builds() -> None:
    workflow = WORKFLOW.read_text(encoding="utf-8")
    release_gate = workflow.split("\n  release-gate:\n", 1)[1]
    required_dependencies = (
        "backend",
        "schema",
        "api-contract",
        "sandbox-contract",
        "web",
        "qa",
        "browser-e2e",
        "release-regressions",
    )
    for dependency in required_dependencies:
        assert f"      - {dependency}" in release_gate
    assert "all(.[]; .result == \"success\")" in release_gate
