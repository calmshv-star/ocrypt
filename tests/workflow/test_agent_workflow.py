"""Behavioral checks for the local agent workflow; no running services required."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("agent_workflow", ROOT / "scripts/agent_workflow.py")
workflow = importlib.util.module_from_spec(spec)
spec.loader.exec_module(workflow)


class WorkflowTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        self.git("init", "-q")
        self.git("config", "user.email", "workflow@example.invalid")
        self.git("config", "user.name", "Workflow Test")
        (self.root / "README.md").write_text("fixture\n")
        (self.root / ".gitignore").write_text(".agent-evidence/\ndocs/tasks/*/state.json\ndocs/tasks/*/.state.lock\n")
        self.git("add", "README.md", ".gitignore")
        self.git("commit", "-qm", "fixture")

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], text=True).strip()

    def init(self, slug="payment-replay"):
        return workflow.init_task(self.root, slug, "Prevent duplicate credit", ROOT / "docs/agent-workflow/templates")

    def test_init_records_base_and_keeps_independent_tasks(self):
        first = self.init()
        second = self.init("rate-quorum")
        state = json.loads((first / "state.json").read_text())
        self.assertEqual(state["base_commit"], self.git("rev-parse", "HEAD"))
        self.assertEqual(state["workers"], {})
        for name in ("SPEC.md", "PLAN.md", "DECISIONS.md"):
            self.assertIn("payment-replay", (first / name).read_text())
            self.assertIn("rate-quorum", (second / name).read_text())

    def test_invalid_and_duplicate_tasks_do_not_overwrite_files(self):
        for slug in ("../escape", "a/b", "", "A", "a" * 60):
            with self.subTest(slug=slug), self.assertRaises(ValueError):
                self.init(slug)
        task = self.init()
        (task / "SPEC.md").write_text("accepted requirements\n")
        with self.assertRaises(FileExistsError):
            self.init()
        self.assertEqual((task / "SPEC.md").read_text(), "accepted requirements\n")

    def test_dirty_base_requires_committed_snapshot(self):
        (self.root / "README.md").write_text("pending change\n")
        with self.assertRaisesRegex(ValueError, "clean"):
            self.init()
        self.assertFalse((self.root / "docs/tasks/payment-replay").exists())

    def test_workers_start_at_task_snapshot_and_isolate_edits(self):
        task = self.init()
        base = json.loads((task / "state.json").read_text())["base_commit"]
        self.git("add", "docs/tasks")
        self.git("commit", "-qm", "task documents")
        first = workflow.add_worker(self.root, "payment-replay", "backend")
        second = workflow.add_worker(self.root, "payment-replay", "tests")
        self.assertEqual(subprocess.check_output(["git", "-C", str(first), "rev-parse", "HEAD"], text=True).strip(), base)
        (first / "README.md").write_text("worker change\n")
        self.assertEqual((self.root / "README.md").read_text(), "fixture\n")
        self.assertEqual((second / "README.md").read_text(), "fixture\n")
        with self.assertRaises(ValueError):
            workflow.add_worker(self.root, "payment-replay", "backend")
        workers = json.loads((task / "state.json").read_text())["workers"]
        self.assertEqual(set(workers), {"backend", "tests"})
        # Registered worktrees are retained; cleanup only removes this fixture.

    def test_invalid_worker_and_corrupted_state_do_not_create_worktrees(self):
        task = self.init()
        with self.assertRaises(ValueError):
            workflow.add_worker(self.root, "payment-replay", "../escape")
        state_path = task / "state.json"
        state = json.loads(state_path.read_text())
        state["base_commit"] = "not-a-commit"
        state_path.write_text(json.dumps(state))
        with self.assertRaises(ValueError):
            workflow.add_worker(self.root, "payment-replay", "backend")
        self.assertEqual(len(self.git("worktree", "list", "--porcelain").split("worktree ")) - 1, 1)

    def test_offline_environment_drops_live_opt_ins_and_credentials(self):
        source = {"PATH": "/usr/bin", "HOME": "/test", "GOCACHE": "/cache", "DATABASE_URL": "private", "MERCHANT_SECRET": "private", "RUN_LIVE_RATE_GATEWAY": "1", "OCRYPT_NATIVE_EVM_REPLAY_LIVE_CONFIG": "/private", "NODE_OPTIONS": "--require /private"}
        clean = workflow.offline_environment(source)
        self.assertEqual(clean["PATH"], "/usr/bin")
        self.assertEqual(clean["GOCACHE"], "/cache")
        for key in ("DATABASE_URL", "MERCHANT_SECRET", "RUN_LIVE_RATE_GATEWAY", "OCRYPT_NATIVE_EVM_REPLAY_LIVE_CONFIG", "NODE_OPTIONS"):
            self.assertNotIn(key, clean)

    def test_failed_check_stops_and_records_failure_instead_of_success(self):
        commands = [("first", self.root, ["ocrypt-workflow-missing-tool-fixture"]), ("second", self.root, ["never-run"])]
        with patch.object(workflow, "profile_commands", return_value=commands):
            code, evidence = workflow.run_checks(self.root, "backend")
        self.assertEqual(code, 127)
        data = json.loads(evidence.read_text())
        self.assertEqual(data["status"], "failed")
        self.assertEqual(len(data["checks"]), 1)
        self.assertEqual(data["checks"][0]["exit_code"], 127)
        self.assertEqual(data["revision"], self.git("rev-parse", "HEAD"))

    def test_snapshot_requires_accepted_documents_to_be_committed(self):
        task = self.init()
        with self.assertRaisesRegex(ValueError, "clean"):
            workflow.snapshot_task(self.root, "payment-replay")
        self.git("add", "docs/tasks")
        self.git("commit", "-qm", "accepted specification")
        base = workflow.snapshot_task(self.root, "payment-replay")
        self.assertEqual(base, self.git("rev-parse", "HEAD"))
        writer = workflow.add_worker(self.root, "payment-replay", "backend")
        (writer / "new-test.txt").write_text("not yet integrated\n")
        subprocess.check_call(["git", "-C", str(writer), "add", "new-test.txt"])
        subprocess.check_call(["git", "-C", str(writer), "commit", "-qm", "worker changes"])
        with self.assertRaisesRegex(ValueError, "integrated"):
            workflow.snapshot_task(self.root, "payment-replay")

    def test_integrated_test_author_can_handoff_to_implementation_in_same_task(self):
        task = self.init()
        self.git("add", "docs/tasks")
        self.git("commit", "-qm", "accepted specification")
        workflow.snapshot_task(self.root, "payment-replay")
        writer = workflow.add_worker(self.root, "payment-replay", "test-author")
        (writer / "regression.txt").write_text("accepted check\n")
        subprocess.check_call(["git", "-C", str(writer), "add", "regression.txt"])
        subprocess.check_call(["git", "-C", str(writer), "commit", "-qm", "regression check"])
        self.git("merge", "--ff-only", "agent/payment-replay/test-author")
        new_base = workflow.snapshot_task(self.root, "payment-replay")
        implementation = workflow.add_worker(self.root, "payment-replay", "backend")
        self.assertTrue((implementation / "regression.txt").exists())
        state = json.loads((task / "state.json").read_text())
        self.assertEqual(state["base_commit"], new_base)
        self.assertEqual(set(state["workers"]), {"backend"})
        self.assertEqual(set(state["completed_workers"]), {"test-author"})

    def test_snapshot_and_allocation_share_a_lock(self):
        task = self.init()
        self.git("add", "docs/tasks")
        self.git("commit", "-qm", "accepted specification")
        observed = []
        def concurrent_allocation(*args, **kwargs):
            with self.assertRaises(FileExistsError):
                workflow.add_worker(self.root, "payment-replay", "backend")
            observed.append(True)
        with patch.object(workflow, "require_clean", side_effect=concurrent_allocation):
            workflow.snapshot_task(self.root, "payment-replay")
        self.assertEqual(observed, [True])
        self.assertEqual(json.loads((task / "state.json").read_text())["workers"], {})

    def test_real_check_result_retains_log_and_dirty_state(self):
        (self.root / "README.md").write_text("pending\n")
        commands = [("acceptance", self.root, [sys.executable, "-c", "print('fixture passed')"])]
        with patch.object(workflow, "profile_commands", return_value=commands):
            code, evidence = workflow.run_checks(self.root, "workflow")
        data = json.loads(evidence.read_text())
        self.assertEqual(code, 0)
        self.assertEqual(data["status"], "passed")
        self.assertTrue(data["dirty"])
        self.assertIn("fixture passed", (evidence.parent / data["checks"][0]["log"]).read_text())


if __name__ == "__main__":
    unittest.main()
