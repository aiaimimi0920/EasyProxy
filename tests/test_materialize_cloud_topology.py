from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "materialize-cloud-topology.py"
SPEC = importlib.util.spec_from_file_location("materialize_cloud_topology", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(MODULE)


class MaterializeCloudTopologyTests(unittest.TestCase):
    def test_overrides_only_cloudflare_resource_names(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "topology.yaml"
            MODULE.materialize(REPO_ROOT / "topology.example.yaml", output, "my-pages", "my-d1")
            payload = yaml.safe_load(output.read_text(encoding="utf-8"))
        expected = yaml.safe_load((REPO_ROOT / "topology.example.yaml").read_text(encoding="utf-8"))
        expected["cloudflare"]["resources"].update(pages_project="my-pages", d1_database="my-d1")
        self.assertEqual(payload, expected)
        self.assertEqual(payload["cloudflare"]["resources"]["pages_project"], "my-pages")
        self.assertEqual(payload["cloudflare"]["resources"]["d1_database"], "my-d1")
        self.assertEqual(payload["secrets"]["misub_backup_passphrase"], "EASYPROXY_BACKUP_PASSPHRASE")
        self.assertTrue(payload["components"]["misub"])

    def test_rejects_empty_resource_names_before_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "topology.yaml"
            for pages, database in ((" ", "db"), ("pages", " ")):
                with self.subTest(pages=pages, database=database):
                    with self.assertRaisesRegex(ValueError, "must not be empty"):
                        MODULE.materialize(REPO_ROOT / "topology.example.yaml", output, pages, database)
                    self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
