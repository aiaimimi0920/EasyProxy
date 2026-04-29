import base64
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


class AggregatorMaterializeTests(unittest.TestCase):
    def run_script(self, template_path: Path, output_path: Path, env=None):
        merged_env = os.environ.copy()
        if env:
            merged_env.update(env)
        return subprocess.run(
            [
                "python",
                str(REPO_ROOT / "scripts" / "materialize-aggregator-config.py"),
                "--template",
                str(template_path),
                "--output",
                str(output_path),
            ],
            cwd=REPO_ROOT,
            env=merged_env,
            capture_output=True,
            text=True,
            timeout=30,
        )

    def test_materialize_replaces_native_placeholders(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            template = Path(temp_dir) / "config.template.json"
            output = Path(temp_dir) / "config.runtime.json"
            template.write_text(
                '{"sub":"https://example.com?token=__TOKEN_PLACEHOLDER__","token":"__TOKEN_PLACEHOLDER__"}',
                encoding="utf-8",
            )

            result = self.run_script(
                template,
                output,
                env={
                    "EASYPROXY_AGGREGATOR_SHARED_TOKEN": "shared-token",
                },
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            rendered = output.read_text(encoding="utf-8")
            self.assertIn("shared-token", rendered)
            self.assertNotIn("PLACEHOLDER", rendered)

    def test_materialize_replaces_base64_url_placeholders(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            template = Path(temp_dir) / "config.template.json"
            output = Path(temp_dir) / "config.runtime.json"
            template.write_text(
                '{"sub":"__ISSUE91_SUB_URL_PLACEHOLDER__"}',
                encoding="utf-8",
            )

            issue91_url = "https://example.com/api/v1/subscribe?token=g6ra!bujhj9vx*gyw4b-l1yh&target=clash&list=1"

            result = self.run_script(
                template,
                output,
                env={
                    "EASYPROXY_AGGREGATOR_ISSUE91_SUB_URL_B64": base64.b64encode(issue91_url.encode("utf-8")).decode(
                        "ascii"
                    ),
                },
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            rendered = output.read_text(encoding="utf-8")
            self.assertIn(issue91_url, rendered)
            self.assertNotIn("PLACEHOLDER", rendered)

    def test_materialize_fails_when_required_secret_missing(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            template = Path(temp_dir) / "config.template.json"
            output = Path(temp_dir) / "config.runtime.json"
            template.write_text(
                '{"domains":[{"name":"placeholder-source","enable":true,"sub":["https://example.com?token=__TOKEN_PLACEHOLDER__"]}]}',
                encoding="utf-8",
            )

            result = self.run_script(template, output)

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            rendered = output.read_text(encoding="utf-8")
            self.assertNotIn("PLACEHOLDER", rendered)
            self.assertIn('"enable": false', rendered)
            self.assertIn("[disabled-missing-secret]", rendered)
