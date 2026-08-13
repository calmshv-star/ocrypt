import pathlib
import subprocess
import sys
import tempfile
import unittest

import install_runtime


class ShowyInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temporary.name)
        (self.root / "server/api/migrations").mkdir(parents=True)
        (self.root / "server/conf").mkdir(parents=True)
        (self.root / "server/api/urls.py").write_text(
            "from .unmatched_views import unmatched_payment_webhook\n"
            '    path("healthz/", healthz, name="healthz"),\n'
        )
        (self.root / "server/api/tasks.py").write_text(install_runtime.POLL_TASK)
        (self.root / "server/conf/settings.py").write_text(
            '''    "poll-showy-crypto-orders": {
        "task": "api.tasks.poll_showy_crypto_orders",
        "schedule": crontab(minute="*", hour="*"),
    },
'''
        )
        (self.root / "docker-compose.yml").write_text(
            '    volumes: *lampac-media-volume\n    shm_size: "512mb"\n'
        )
        self.installer = pathlib.Path(install_runtime.__file__).resolve()

    def tearDown(self):
        self.temporary.cleanup()

    def run_phase(self, phase):
        subprocess.run(
            [sys.executable, str(self.installer), phase, str(self.root)],
            check=True,
            capture_output=True,
            text=True,
        )

    def test_receiver_is_idempotent_and_finalize_is_separate(self):
        self.run_phase("receiver")
        self.run_phase("receiver")

        tasks = (self.root / "server/api/tasks.py").read_text()
        settings = (self.root / "server/conf/settings.py").read_text()
        self.assertIn("def poll_showy_crypto_orders", tasks)
        self.assertIn("def provision_showy_crypto_checkout", tasks)
        self.assertIn('"poll-showy-crypto-orders"', settings)
        self.assertTrue((self.root / "server/api/ocrypt_webhook.py").is_file())

        self.run_phase("finalize")
        self.run_phase("finalize")

        tasks = (self.root / "server/api/tasks.py").read_text()
        settings = (self.root / "server/conf/settings.py").read_text()
        self.assertNotIn("def poll_showy_crypto_orders", tasks)
        self.assertIn("def provision_showy_crypto_checkout", tasks)
        self.assertNotIn('"poll-showy-crypto-orders"', settings)


if __name__ == "__main__":
    unittest.main()
