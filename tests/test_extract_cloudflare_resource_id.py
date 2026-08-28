from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "extract-cloudflare-resource-id.py"
SPEC = importlib.util.spec_from_file_location("extract_cloudflare_resource_id", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(MODULE)


class ExtractCloudflareResourceIDTests(unittest.TestCase):
    def test_supports_nested_d1_create_response(self) -> None:
        self.assertEqual(MODULE.resource_ids({"result": {"database_id": "db-id"}}), {"db-id"})

    def test_reports_ambiguous_ids(self) -> None:
        self.assertEqual(MODULE.resource_ids([{"uuid": "one"}, {"id": "two"}]), {"one", "two"})

    def test_resolves_one_exact_named_resource_from_result(self) -> None:
        payload = {"result": [{"name": "other", "uuid": "a"}, {"name": "target", "database_id": "b"}]}
        self.assertEqual(MODULE.exact_named_resource_id(payload, "target"), "b")

    def test_rejects_duplicate_named_resources(self) -> None:
        with self.assertRaisesRegex(ValueError, "exactly one"):
            MODULE.exact_named_resource_id([{"name": "x", "uuid": "a"}, {"name": "x", "uuid": "b"}], "x")


if __name__ == "__main__":
    unittest.main()
