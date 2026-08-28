import importlib.util
import os
import unittest
from pathlib import Path
from unittest.mock import patch


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = REPO_ROOT / "scripts" / "verify-cloudflare-worker-identity.py"
spec = importlib.util.spec_from_file_location("verify_cloudflare_worker_identity", SCRIPT_PATH)
worker_identity = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(worker_identity)


class FakeResponse:
    def __init__(self, status_code: int, payload=None):
        self.status_code = status_code
        self.payload = payload or {}

    def raise_for_status(self):
        if self.status_code >= 400:
            raise RuntimeError(f"HTTP {self.status_code}")

    def json(self):
        return self.payload


class FakeSession:
    def __init__(self, response):
        self.response = response

    def get(self, *_args, **_kwargs):
        return self.response


class VerifyCloudflareWorkerIdentityTests(unittest.TestCase):
    @patch.dict(os.environ, {"CLOUDFLARE_API_TOKEN": "token"}, clear=True)
    def test_update_requires_existing_exact_worker(self):
        with self.assertRaisesRegex(RuntimeError, "refuses to create"):
            worker_identity.verify_identity(FakeSession(FakeResponse(404)), "account", "worker", "update")

    @patch.dict(os.environ, {"CLOUDFLARE_API_TOKEN": "token"}, clear=True)
    def test_bootstrap_allows_missing_worker(self):
        self.assertFalse(
            worker_identity.verify_identity(FakeSession(FakeResponse(404)), "account", "worker", "bootstrap")
        )

    @patch.dict(os.environ, {"CLOUDFLARE_API_TOKEN": "token"}, clear=True)
    def test_existing_worker_requires_success_envelope(self):
        self.assertTrue(
            worker_identity.verify_identity(
                FakeSession(FakeResponse(200, {"success": True})), "account", "worker", "update"
            )
        )


if __name__ == "__main__":
    unittest.main()
