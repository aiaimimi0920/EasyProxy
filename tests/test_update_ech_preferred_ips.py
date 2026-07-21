import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "deploy" / "service" / "base" / "scripts" / "update_ech_preferred_ips.ps1"


class UpdateEchPreferredIpsTests(unittest.TestCase):
    def test_script_has_no_implicit_private_archive_dependency(self):
        script_text = SCRIPT.read_text(encoding="utf-8")
        self.assertNotIn("AIRead", script_text)
        self.assertNotIn("MiSub密钥", script_text)

    def run_script(self, args):
        return subprocess.run(
            ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(SCRIPT), *args],
            cwd=REPO_ROOT,
            env=os.environ.copy(),
            capture_output=True,
            text=True,
            timeout=120,
        )

    def write_fixture(self, temp_dir):
        root_config = {
            "misub": {
                "pages": {
                    "publicUrl": "https://misub.example.test",
                    "connectorProfileId": "config-profile",
                },
                "docker": {
                    "env": {
                        "ADMIN_PASSWORD": "config-admin",
                        "MANIFEST_TOKEN": "config-manifest",
                    }
                },
            },
            "echWorkersCloudflare": {
                "publicUrl": "https://worker.example.test",
                "secrets": {"ECH_TOKEN": "config-worker-token"},
            },
        }
        config_path = Path(temp_dir) / "config.yaml"
        config_path.write_text(yaml.safe_dump(root_config, sort_keys=False), encoding="utf-8")

        result_csv = Path(temp_dir) / "result.csv"
        result_csv.write_text(
            "IP 地址,平均延迟,丢包率,下载速度(MB/s),地区码\n"
            "203.0.113.10,20,0,2.5,TEST\n",
            encoding="utf-8",
        )
        return config_path, result_csv

    def test_root_config_supplies_ech_and_misub_defaults_without_private_archive(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            config_path, result_csv = self.write_fixture(temp_dir)
            artifact_root = Path(temp_dir) / "artifacts"
            result = self.run_script(
                [
                    "-ConfigPath",
                    str(config_path),
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(artifact_root),
                    "-TopCount",
                    "1",
                ]
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["profile_id"], "config-profile")
            self.assertEqual(summary["worker_url"], "https://worker.example.test:443")
            self.assertEqual(summary["worker_url_source"], "root_config")
            selected = json.loads((Path(summary["artifact_dir"]) / "selected-sources.json").read_text(encoding="utf-8-sig"))
            self.assertEqual(selected[0]["connector_config"]["access_token"], "config-worker-token")

    def test_explicit_values_override_root_config_values(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            config_path, result_csv = self.write_fixture(temp_dir)
            artifact_root = Path(temp_dir) / "artifacts"
            result = self.run_script(
                [
                    "-ConfigPath",
                    str(config_path),
                    "-ProfileId",
                    "explicit-profile",
                    "-WorkerUrl",
                    "https://explicit.example.test:8443",
                    "-AccessToken",
                    "explicit-token",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(artifact_root),
                    "-TopCount",
                    "1",
                ]
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["profile_id"], "explicit-profile")
            self.assertEqual(summary["worker_url"], "https://explicit.example.test:8443")
            self.assertEqual(summary["worker_url_source"], "explicit")
            selected = json.loads((Path(summary["artifact_dir"]) / "selected-sources.json").read_text(encoding="utf-8-sig"))
            self.assertEqual(selected[0]["connector_config"]["access_token"], "explicit-token")

    def test_explicit_values_work_with_partial_root_config(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            config_path = Path(temp_dir) / "config.yaml"
            config_path.write_text("unrelated: true\n", encoding="utf-8")
            _, result_csv = self.write_fixture(temp_dir)
            artifact_root = Path(temp_dir) / "artifacts"
            result = self.run_script(
                [
                    "-ConfigPath",
                    str(config_path),
                    "-ProfileId",
                    "explicit-profile",
                    "-WorkerUrl",
                    "https://explicit.example.test:443",
                    "-AccessToken",
                    "explicit-token",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(artifact_root),
                ]
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["profile_id"], "explicit-profile")
            self.assertEqual(summary["worker_url_source"], "explicit")

    def test_prefer_custom_domain_uses_root_config_without_hardcoded_deployment_url(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            config_path, result_csv = self.write_fixture(temp_dir)
            artifact_root = Path(temp_dir) / "artifacts"
            result = self.run_script(
                [
                    "-ConfigPath",
                    str(config_path),
                    "-PreferCustomDomain",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(artifact_root),
                ]
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["worker_url"], "https://worker.example.test:443")
            self.assertEqual(summary["worker_url_source"], "root_config")

    def test_reused_csv_parsing_does_not_depend_on_header_encoding(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            config_path, result_csv = self.write_fixture(temp_dir)
            result_csv.write_text(
                "column_1,column_2,column_3,column_4,column_5\n"
                "203.0.113.10,20,0,2.5,TEST\n",
                encoding="utf-8",
            )
            artifact_root = Path(temp_dir) / "artifacts"

            result = self.run_script(
                [
                    "-ConfigPath",
                    str(config_path),
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(artifact_root),
                    "-TopCount",
                    "1",
                ]
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            selected = json.loads(
                (Path(summary["artifact_dir"]) / "selected-sources.json").read_text(encoding="utf-8-sig")
            )
            self.assertEqual(selected[0]["connector_config"]["server_ip"], "203.0.113.10")


if __name__ == "__main__":
    unittest.main()
