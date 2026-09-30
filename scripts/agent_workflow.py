#!/usr/bin/env python3
"""Task context, pinned Git worktrees, and credential-free local checks."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import uuid


ROOT = Path(__file__).resolve().parents[1]
SLUG = re.compile(r"[a-z0-9][a-z0-9-]{0,47}\Z")
ENV_KEYS = {
    "PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "SYSTEMROOT",
    "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TERM", "GOCACHE", "GOMODCACHE",
    "GOPATH", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "PNPM_HOME",
}


def validate_slug(value):
    if not SLUG.fullmatch(value):
        raise ValueError("Use 1-48 lowercase letters, digits, or hyphens; start with a letter or digit")
    return value


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True, stderr=subprocess.PIPE).strip()


def write_json(path, value):
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False, encoding="utf-8") as file:
        file.write(json.dumps(value, indent=2) + "\n")
        temporary = Path(file.name)
    temporary.replace(path)


def require_clean(root, ignore_task_documents=False):
    args = ["status", "--porcelain"]
    if ignore_task_documents:
        args.extend(["--", ".", ":(exclude)docs/tasks"])
    if git(root, *args):
        raise ValueError("A clean committed base is required; preserve and commit task-owned changes first")


def init_task(root, slug, goal, templates):
    validate_slug(slug)
    task = root / "docs/tasks" / slug
    if task.exists():
        raise FileExistsError(f"Task already exists: {task}")
    # Other task documents may be in progress; only source changes make the
    # starting code snapshot ambiguous. snapshot_task later requires all
    # accepted documents/tests to be committed too.
    require_clean(root, ignore_task_documents=True)
    base = git(root, "rev-parse", "HEAD")
    # Read everything before creating the directory, so missing templates do not
    # leave a task half-initialized.
    documents = {name: (templates / name).read_text(encoding="utf-8") for name in ("SPEC.md", "PLAN.md", "DECISIONS.md")}
    task.mkdir(parents=True)
    for name, text in documents.items():
        for key, value in {"task": slug, "goal": goal, "base": base}.items():
            text = text.replace("{{" + key + "}}", value)
        (task / name).write_text(text, encoding="utf-8")
    write_json(task / "state.json", {"schema": 1, "task": slug, "base_commit": base, "workers": {}})
    return task


def load_state(root, slug):
    validate_slug(slug)
    path = root / "docs/tasks" / slug / "state.json"
    state = json.loads(path.read_text(encoding="utf-8"))
    if state.get("schema") != 1 or state.get("task") != slug or not isinstance(state.get("workers"), dict):
        raise ValueError("Invalid local task state; inspect it before continuing")
    if not re.fullmatch(r"[0-9a-f]{40,64}", state.get("base_commit", "")):
        raise ValueError("Invalid task base commit")
    git(root, "cat-file", "-e", state["base_commit"] + "^{commit}")
    return path, state


@contextmanager
def task_lock(state_path):
    lock = state_path.parent / ".state.lock"
    descriptor = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(descriptor)
    try:
        yield
    finally:
        lock.unlink()


def snapshot_task(root, slug):
    path, _ = load_state(root, slug)
    with task_lock(path):
        path, state = load_state(root, slug)
        require_clean(root)
        base = git(root, "rev-parse", "HEAD")
        for worker, record in state["workers"].items():
            directory = Path(record["path"])
            if git(directory, "status", "--porcelain"):
                raise ValueError(f"Worker {worker} has pending changes; preserve and integrate them first")
            revision = git(directory, "rev-parse", "HEAD")
            try:
                git(root, "merge-base", "--is-ancestor", revision, base)
            except subprocess.CalledProcessError as error:
                raise ValueError(f"Worker {worker} must be integrated before updating the common base") from error
        completed = state.setdefault("completed_workers", {})
        completed.update(state["workers"])
        state["workers"] = {}
        state["base_commit"] = base
        write_json(path, state)
        return base


def add_worker(root, slug, worker):
    validate_slug(worker)
    state_path, state = load_state(root, slug)
    if worker in state["workers"] or worker in state.get("completed_workers", {}):
        raise ValueError("Worker already exists; inspect status instead of creating another")
    branch = f"agent/{slug}/{worker}"
    directory = root.parent / ".ocrypt-agent-worktrees" / slug / worker
    if directory.exists():
        raise FileExistsError(f"Worktree directory already exists: {directory}")
    # The orchestrator normally allocates sequentially. This also makes an
    # accidental concurrent allocation fail instead of losing a worker record.
    with task_lock(state_path):
        state_path, state = load_state(root, slug)
        if worker in state["workers"] or worker in state.get("completed_workers", {}):
            raise ValueError("Worker already exists")
        directory.parent.mkdir(parents=True, exist_ok=True)
        git(root, "worktree", "add", "-b", branch, str(directory), state["base_commit"])
        state["workers"][worker] = {"branch": branch, "path": str(directory.resolve()), "base_commit": state["base_commit"]}
        write_json(state_path, state)
    return directory.resolve()


def offline_environment(source):
    return {key: value for key, value in source.items() if key in ENV_KEYS}


def profile_commands(root, profile):
    python = sys.executable
    financial = ["./internal/money", "./internal/domain", "./internal/providers", "./internal/scanner", "./internal/application", "./internal/adapters/postgres", "./internal/auth", "./internal/webhook", "./internal/refunds", "./internal/treasury"]
    concurrency = ["./internal/providers", "./internal/scanner", "./internal/application", "./internal/adapters/postgres"]
    commands = {
        "workflow": [("workflow unit tests", root, [python, "-m", "unittest", "discover", "-s", "tests/workflow", "-v"])],
        "python": [("offline Python tests", root, [python, "-m", "pytest", "-q", "tests/unit", "tests/security", "tests/i18n", "tests/release", "tests/contract/test_sdk_surface.py", "tests/contract/test_framework_examples.py"])],
        "backend": [("Go tests", root / "backend", ["go", "test", "./..."]), ("Go build", root / "backend", ["go", "build", "./cmd/..."]), ("Go vet", root / "backend", ["go", "vet", "./..."]), ("Go race", root / "backend", ["go", "test", "-race", "./..."])],
        "financial": [("financial tests", root / "backend", ["go", "test", *financial]), ("financial vet", root / "backend", ["go", "vet", *concurrency]), ("financial race", root / "backend", ["go", "test", "-race", *concurrency]), ("identity fuzz", root / "backend", ["go", "test", "./internal/providers", "-run", "^$", "-fuzz", "^FuzzCanonicalIdentityNormalization$", "-fuzztime=5s"]), ("aggregation fuzz", root / "backend", ["go", "test", "./internal/application", "-run", "^$", "-fuzz", "^FuzzEvaluateAutomatedMatchAggregation$", "-fuzztime=5s"])],
        "web": [("web typecheck", root, ["pnpm", "typecheck"]), ("web tests", root, ["pnpm", "test"]), ("web build", root, ["pnpm", "build"])],
    }
    return commands[profile]


def run_checks(root, profile):
    commands = profile_commands(root, profile)
    revision = git(root, "rev-parse", "HEAD")
    dirty = bool(git(root, "status", "--porcelain"))
    evidence = root / ".agent-evidence" / (datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + profile + "-" + uuid.uuid4().hex[:8])
    evidence.mkdir(parents=True)
    report = {"profile": profile, "revision": revision, "dirty": dirty, "status": "running", "checks": []}
    env = offline_environment(os.environ)
    report_path = evidence / "result.json"
    write_json(report_path, report)
    code = 0
    for index, (label, cwd, argv) in enumerate(commands):
        print(f"Running {label}", flush=True)
        try:
            result = subprocess.run(argv, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            code, output = result.returncode, result.stdout
        except OSError as error:
            code, output = 127, f"Cannot run {label}: {error}\n"
        log = evidence / f"{index + 1}.log"
        log.write_text(output, encoding="utf-8")
        print(output, end="", flush=True)
        report["checks"].append({"name": label, "argv": argv, "cwd": str(cwd), "exit_code": code, "log": log.name})
        report["status"] = "failed" if code else "running"
        write_json(report_path, report)
        if code:
            break
    report["status"] = "failed" if code else "passed"
    write_json(report_path, report)
    print(f"{report['status']}: {report_path}", flush=True)
    return code if code >= 0 else 1, report_path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    init = sub.add_parser("init", help="Create task specification, plan, and decisions without overwriting existing tasks")
    init.add_argument("task")
    init.add_argument("--goal", required=True)
    for name in ("snapshot", "status"):
        sub.add_parser(name).add_argument("task")
    worker = sub.add_parser("worker", help="Create a writer worktree from the pinned task snapshot")
    worker.add_argument("task")
    worker.add_argument("worker")
    check = sub.add_parser("check", help="Run a local check profile without inherited live opt-ins or credentials")
    check.add_argument("profile", choices=("workflow", "python", "backend", "financial", "web"))
    args = parser.parse_args()
    try:
        if args.command == "init":
            print(init_task(ROOT, args.task, args.goal, ROOT / "docs/agent-workflow/templates"))
        elif args.command == "snapshot":
            print(snapshot_task(ROOT, args.task))
        elif args.command == "worker":
            print(add_worker(ROOT, args.task, args.worker))
        elif args.command == "status":
            _, state = load_state(ROOT, args.task)
            print(json.dumps(state, indent=2))
        else:
            return run_checks(ROOT, args.profile)[0]
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"Workflow stopped: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
