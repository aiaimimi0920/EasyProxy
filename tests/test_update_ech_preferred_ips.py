import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "deploy/service/base/scripts/update_ech_preferred_ips.ps1"
TOPOLOGY = REPO_ROOT / "topology.example.yaml"


class UpdateEchPreferredIpsTests(unittest.TestCase):
    def test_script_has_no_implicit_private_archive_dependency(self):
        script_text = SCRIPT.read_text(encoding="utf-8")
        self.assertNotIn("AIRead", script_text)
        self.assertNotIn("MiSub密钥", script_text)
        self.assertNotIn("config.yaml", script_text)

    def run_script(self, args, env=None):
        merged_env = os.environ.copy()
        if env:
            merged_env.update(env)
        return subprocess.run(
            ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(SCRIPT), *args],
            cwd=REPO_ROOT,
            env=merged_env,
            capture_output=True,
            text=True,
            timeout=120,
        )

    def write_result_csv(self, temp_dir, header="IP 地址,平均延迟,丢包率,下载速度(MB/s),地区码"):
        result_csv = Path(temp_dir) / "result.csv"
        result_csv.write_text(
            f"{header}\n203.0.113.10,20,0,2.5,TEST\n",
            encoding="utf-8",
        )
        return result_csv

    def topology_env(self):
        return {
            "MISUB_ADMIN_PASSWORD": "test-admin",
            "MISUB_MANIFEST_TOKEN": "test-manifest",
            "ECH_TOKEN": "test-worker-token",
        }

    def test_topology_supplies_profile_and_secret_reference(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            result_csv = self.write_result_csv(temp_dir)
            result = self.run_script(
                [
                    "-TopologyPath",
                    str(TOPOLOGY),
                    "-WorkerUrl",
                    "https://worker.example.test:443",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(Path(temp_dir) / "artifacts"),
                    "-TopCount",
                    "1",
                ],
                env=self.topology_env(),
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["profile_id"], "easyproxies-ech-runtime")
            self.assertEqual(summary["worker_url_source"], "explicit")
            artifact_dir = Path(summary["artifact_dir"])
            self.assertFalse((artifact_dir / "selected-sources.json").exists())
            self.assertNotIn("test-worker-token", "".join(
                path.read_text(encoding="utf-8-sig")
                for path in artifact_dir.iterdir()
                if path.is_file()
            ))

    def test_explicit_values_override_topology_values(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            result_csv = self.write_result_csv(temp_dir)
            result = self.run_script(
                [
                    "-TopologyPath",
                    str(TOPOLOGY),
                    "-ProfileId",
                    "explicit-profile",
                    "-WorkerUrl",
                    "https://explicit.example.test:8443",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(Path(temp_dir) / "artifacts"),
                ],
                env=self.topology_env(),
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["profile_id"], "explicit-profile")
            self.assertEqual(summary["worker_url"], "https://explicit.example.test:8443")
            self.assertNotIn("test-worker-token", result.stdout)

    def test_topology_is_required_for_secret_resolution(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            result_csv = self.write_result_csv(temp_dir)
            result = self.run_script(
                [
                    "-TopologyPath",
                    str(Path(temp_dir) / "missing-topology.yaml"),
                    "-WorkerUrl",
                    "https://explicit.example.test:443",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(Path(temp_dir) / "artifacts"),
                ]
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("topology", f"{result.stdout}\n{result.stderr}".lower())

    def test_prefer_custom_domain_requires_no_hardcoded_deployment_url(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            result_csv = self.write_result_csv(temp_dir)
            result = self.run_script(
                [
                    "-TopologyPath",
                    str(TOPOLOGY),
                    "-CustomDomainUrl",
                    "https://worker.example.test",
                    "-PreferCustomDomain",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(Path(temp_dir) / "artifacts"),
                ],
                env=self.topology_env(),
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["worker_url"], "https://worker.example.test:443")
            self.assertEqual(summary["worker_url_source"], "custom_domain_override")

    def test_reused_csv_parsing_does_not_depend_on_header_encoding(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            result_csv = self.write_result_csv(temp_dir, "column_1,column_2,column_3,column_4,column_5")
            result = self.run_script(
                [
                    "-TopologyPath",
                    str(TOPOLOGY),
                    "-WorkerUrl",
                    "https://worker.example.test:443",
                    "-ReuseResultCsvPath",
                    str(result_csv),
                    "-ArtifactRoot",
                    str(Path(temp_dir) / "artifacts"),
                    "-TopCount",
                    "1",
                ],
                env=self.topology_env(),
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            summary = json.loads(result.stdout)
            self.assertEqual(summary["selected_ips"][0]["ip"], "203.0.113.10")


if __name__ == "__main__":
    unittest.main()
