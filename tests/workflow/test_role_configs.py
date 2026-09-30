"""Validate the configuration consumed by project-scoped Codex subagents."""

from pathlib import Path
import tomllib
import unittest


ROOT = Path(__file__).resolve().parents[2]


class RoleConfigTest(unittest.TestCase):
    def test_distinct_roles_use_supported_models_and_inherit_permissions(self):
        names = set()
        for path in sorted((ROOT / ".codex/agents").glob("*.toml")):
            with self.subTest(role=path.name):
                role = tomllib.loads(path.read_text())
                self.assertNotIn(role["name"], names)
                names.add(role["name"])
                self.assertTrue(role["description"].strip())
                self.assertTrue(role["developer_instructions"].strip())
                self.assertIn(role["model"], {"gpt-6-astra", "gpt-6.1-sol"})
                self.assertIn(role["model_reasoning_effort"], {"low", "high"})
                for setting in ("approval_policy", "sandbox_mode", "mcp_servers"):
                    self.assertNotIn(setting, role)
        self.assertEqual(names, {"ocrypt_orchestrator", "ocrypt_test_author", "ocrypt_worker", "ocrypt_reviewer"})

    def test_project_config_limits_only_project_subagents(self):
        config = tomllib.loads((ROOT / ".codex/config.toml").read_text())
        self.assertEqual(config, {"agents": {"enabled": True, "max_concurrent_threads_per_session": 3}})


if __name__ == "__main__":
    unittest.main()
