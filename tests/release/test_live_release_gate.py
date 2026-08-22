from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parents[2]
GATE = ROOT / "scripts" / "verify-live-release.sh"
REVISION = "1" * 40


def write_executable(path: Path, body: str) -> None:
    path.write_text(body, encoding="utf-8")
    path.chmod(0o755)


def release_fixture(tmp_path: Path) -> tuple[Path, Path, dict[str, str]]:
    release_file = tmp_path / "admin.js"
    release_file.write_text("immutable admin release\n", encoding="utf-8")
    digest = hashlib.sha256(release_file.read_bytes()).hexdigest()
    manifest = tmp_path / "live-release.json"
    manifest.write_text(
        json.dumps(
            {
                "expected_revision": REVISION,
                "containers": [{"name": "ocrypt-admin-api", "max_restarts": 0}],
                "http_checks": [
                    {
                        "url": "https://admin.example.test/admin/",
                        "status": 200,
                        "timeout_seconds": 3,
                    }
                ],
                "files": [{"path": str(release_file), "sha256": digest}],
            }
        ),
        encoding="utf-8",
    )
    tools = tmp_path / "bin"
    tools.mkdir()
    write_executable(
        tools / "docker",
        """#!/bin/sh
case "$*" in
  *State.Running*) printf '%s\n' "${FAKE_RUNNING:-true}" ;;
  *RestartCount*) printf '%s\n' "${FAKE_RESTARTS:-0}" ;;
  *org.opencontainers.image.revision*) printf '%s\n' "${FAKE_REVISION}" ;;
  *) exit 2 ;;
esac
""",
    )
    write_executable(
        tools / "curl",
        """#!/bin/sh
printf '%s' "${FAKE_HTTP_STATUS:-200}"
""",
    )
    environment = {
        **os.environ,
        "PATH": f"{tools}{os.pathsep}{os.environ.get('PATH', '')}",
        "FAKE_REVISION": REVISION,
        "FAKE_RESTARTS": "0",
        "FAKE_RUNNING": "true",
        "FAKE_HTTP_STATUS": "200",
    }
    return manifest, release_file, environment


def run_gate(manifest: Path, environment: dict[str, str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(GATE), str(manifest)],
        cwd=ROOT,
        env=environment,
        text=True,
        capture_output=True,
        check=False,
    )


def test_live_release_gate_accepts_exact_immutable_release(tmp_path: Path) -> None:
    manifest, _, environment = release_fixture(tmp_path)
    result = run_gate(manifest, environment)
    assert result.returncode == 0, result.stderr
    assert f"revision={REVISION}" in result.stdout
    assert "containers=1 http=1 files=1" in result.stdout


def test_live_release_gate_rejects_wrong_container_revision(tmp_path: Path) -> None:
    manifest, _, environment = release_fixture(tmp_path)
    environment["FAKE_REVISION"] = "2" * 40
    result = run_gate(manifest, environment)
    assert result.returncode == 1
    assert "revision mismatch" in result.stderr


def test_live_release_gate_rejects_restarted_container(tmp_path: Path) -> None:
    manifest, _, environment = release_fixture(tmp_path)
    environment["FAKE_RESTARTS"] = "1"
    result = run_gate(manifest, environment)
    assert result.returncode == 1
    assert "restart count" in result.stderr


def test_live_release_gate_rejects_http_failure(tmp_path: Path) -> None:
    manifest, _, environment = release_fixture(tmp_path)
    environment["FAKE_HTTP_STATUS"] = "502"
    result = run_gate(manifest, environment)
    assert result.returncode == 1
    assert "returned HTTP 502, expected 200" in result.stderr


def test_live_release_gate_rejects_changed_static_file(tmp_path: Path) -> None:
    manifest, release_file, environment = release_fixture(tmp_path)
    release_file.write_text("changed after manifest creation\n", encoding="utf-8")
    result = run_gate(manifest, environment)
    assert result.returncode == 1
    assert "file hash mismatch" in result.stderr


def test_live_release_manifest_example_remains_fail_closed() -> None:
    example = json.loads(
        (ROOT / "deploy" / "release" / "live-manifest.example.json").read_text(
            encoding="utf-8"
        )
    )
    assert example["expected_revision"] == "REPLACE_WITH_40_HEX_GIT_SHA"
    assert all("REPLACE_WITH" in item["sha256"] for item in example["files"])
