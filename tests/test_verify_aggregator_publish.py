import base64
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch


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

    def run_verifier(self, flag, v2ray=b"", effective=None, v2ray_key="v2ray.txt", effective_json=None):
        payloads = {
            "clash.yaml": b"proxies: []",
            "singbox.json": b'{"outbounds": []}',
            "mixed.txt": b"ss://test-node",
            "sources.json": b'{"sources": []}',
            "v2ray.txt": v2ray,
        }
        if effective is not None:
            payloads["effective.txt"] = effective
        if effective_json is not None:
            payloads["effective.json"] = effective_json
        items = {
            "public-clash": "clash.yaml", "public-singbox": "singbox.json",
            "public-mixed": "mixed.txt", "crawledsubs": "sources.json",
            "public-v2ray": v2ray_key, "public-effective": "effective.txt",
            "public-effective-json": "effective.json",
        }
        requested = []

        def fetch(url, **kwargs):
            key = url.rsplit("/", 1)[-1]
            requested.append(key)
            response = Mock(content=payloads.get(key, b""), status_code=200 if key in payloads else 404)
            if response.status_code == 404:
                response.raise_for_status.side_effect = RuntimeError("HTTP 404")
            return response

        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory) / "runtime.json"
            runtime.write_text(json.dumps({"storage": {"items": {name: {"key": key} for name, key in items.items()}}}), encoding="utf-8")
            argv = ["verify", "--base-url", "https://example.invalid", "--runtime-config", str(runtime)]
            if flag:
                argv.append(flag)
            with patch.object(MODULE.sys, "argv", argv), patch.object(MODULE.requests, "get", side_effect=fetch):
                MODULE.main()
        return requested

    def test_pre_audit_rejects_empty_v2ray_with_artifact_name(self):
        with self.assertRaisesRegex(RuntimeError, "public-v2ray: response body is empty"):
            self.run_verifier("--before-effective-audit")

    def test_pre_audit_defers_only_effective_artifacts(self):
        requested = self.run_verifier("--before-effective-audit", base64.b64encode(b"ss://test-node"))
        self.assertEqual(requested, ["clash.yaml", "singbox.json", "mixed.txt", "sources.json", "v2ray.txt"])

    def test_full_verification_still_requires_effective(self):
        with self.assertRaisesRegex(RuntimeError, "public-effective: HTTP 404"):
            self.run_verifier("--require-all", base64.b64encode(b"ss://test-node"))

    def test_full_verification_still_validates_effective_json(self):
        with self.assertRaisesRegex(RuntimeError, "public-effective-json: JSON payload is empty"):
            self.run_verifier("--require-all", base64.b64encode(b"ss://test-node"), b"ss://test-node", effective_json=b"{}")

    def test_default_bootstrap_still_allows_optional_absence(self):
        requested = self.run_verifier(None)
        self.assertIn("effective.json", requested)

    def test_pre_audit_rejects_missing_configured_v2ray_key(self):
        with self.assertRaisesRegex(RuntimeError, "public-v2ray.*key"):
            self.run_verifier("--before-effective-audit", v2ray_key="")

    def test_workflow_checks_generated_artifacts_before_audit(self):
        workflow = (REPO_ROOT / ".github/workflows/deploy-aggregator.yml").read_text(encoding="utf-8")
        before_audit = workflow.split("- name: Audit shared effective aggregator nodes", 1)[0]
        self.assertIn("--before-effective-audit", before_audit)
        final_check = workflow.split("- name: Verify complete candidate release", 1)[1].split("- name:", 1)[0]
        self.assertIn("--require-all", final_check)


if __name__ == "__main__":
    unittest.main()
