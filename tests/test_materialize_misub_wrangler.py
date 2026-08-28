from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


class MaterializeMiSubWranglerTests(unittest.TestCase):
    def test_writes_exact_resource_identity(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "wrangler.jsonc"
            subprocess.run(
                [
                    sys.executable,
                    str(REPO_ROOT / "scripts" / "materialize-misub-wrangler.py"),
                    "--base-config",
                    str(REPO_ROOT / "upstreams" / "misub" / "wrangler.jsonc"),
                    "--output",
                    str(output),
                    "--project-name",
                    "demo-pages",
                    "--deployment-name",
                    "demo",
                    "--public-url",
                    "https://demo.example",
                    "--callback-url",
                    "https://demo.example/callback",
                    "--d1-database-name",
                    "demo-d1",
                    "--d1-database-id",
                    "database-id",
                ],
                check=True,
            )
            payload = json.loads(output.read_text(encoding="utf-8"))
        self.assertEqual(payload["d1_databases"], [{"binding": "MISUB_DB", "database_name": "demo-d1", "database_id": "database-id"}])
        self.assertEqual(payload["vars"]["EASYPROXY_DEPLOYMENT_NAME"], "demo")
        self.assertEqual(payload["vars"]["EASYPROXY_MISUB_D1_DATABASE_ID"], "database-id")


if __name__ == "__main__":
    unittest.main()
