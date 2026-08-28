import base64
import importlib.util
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "verify_aggregator_publish",
    REPO_ROOT / "scripts" / "verify-aggregator-publish.py",
)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class VerifyAggregatorPublishTests(unittest.TestCase):
    def test_accepts_base64_proxy_subscription(self):
        body = base64.b64encode(b"vmess://node-a\nvless://node-b\n")
        MODULE.validate_v2ray_payload(body)

    def test_rejects_arbitrary_nonempty_text(self):
        with self.assertRaisesRegex(RuntimeError, "V2Ray subscription"):
            MODULE.validate_v2ray_payload(b"this is not base64")

    def test_rejects_base64_without_proxy_uris(self):
        body = base64.b64encode(b"ordinary text")
        with self.assertRaisesRegex(RuntimeError, "supported proxy URIs"):
            MODULE.validate_v2ray_payload(body)


if __name__ == "__main__":
    unittest.main()
