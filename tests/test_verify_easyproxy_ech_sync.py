import importlib.util
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT_PATH = REPO_ROOT / "scripts" / "verify-easyproxy-ech-sync.py"
spec = importlib.util.spec_from_file_location("verify_easyproxy_ech_sync", SCRIPT_PATH)
verify_easyproxy_ech_sync = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(verify_easyproxy_ech_sync)


class VerifyEasyProxyECHSyncTests(unittest.TestCase):
    def test_requires_healthy_manifest_and_matching_worker_token(self):
        status = {"manifest_healthy": True, "last_error": "", "connector_instance_count": 1}
        connectors = {
            "connectors": [
                {
                    "connector_type": "ech_worker",
                    "input": "https://worker.example:443",
                    "connector_config": {"access_token": "new-token"},
                }
            ]
        }
        settings = {"source_sync_manifest_url": "https://misub.example/api/manifest/aggregator-global"}
        self.assertTrue(
            verify_easyproxy_ech_sync.synchronized(
                status,
                settings,
                connectors,
                "https://worker.example:443",
                "new-token",
                "aggregator-global",
            )
        )
        self.assertFalse(
            verify_easyproxy_ech_sync.synchronized(
                status,
                settings,
                connectors,
                "https://worker.example:443",
                "old-token",
                "aggregator-global",
            )
        )

    def test_rejects_a_different_manifest_profile(self):
        status = {"manifest_healthy": True, "connector_instance_count": 1}
        settings = {"source_sync_manifest_url": "https://misub.example/api/manifest/other"}
        self.assertFalse(
            verify_easyproxy_ech_sync.synchronized(
                status, settings, {"connectors": []}, "worker", "token", "aggregator-global"
            )
        )

    def test_rejects_degraded_source_sync(self):
        status = {"manifest_healthy": False, "connector_instance_count": 1}
        self.assertFalse(
            verify_easyproxy_ech_sync.synchronized(status, {}, {"connectors": []}, "url", "token", "profile")
        )


if __name__ == "__main__":
    unittest.main()
