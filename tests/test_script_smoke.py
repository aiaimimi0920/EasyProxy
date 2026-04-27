import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


def read_json_lines(path: Path):
    if not path.exists():
        return []
    lines = [line.strip() for line in path.read_text(encoding="utf-8-sig").splitlines() if line.strip()]
    return [json.loads(line) for line in lines]


class ScriptSmokeTests(unittest.TestCase):
    def run_powershell(self, args, env=None):
        merged_env = os.environ.copy()
        if env:
            merged_env.update(env)
        return subprocess.run(
            ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", *args],
            cwd=REPO_ROOT,
            env=merged_env,
            capture_output=True,
            text=True,
            timeout=120,
        )

    def test_deploy_subproject_dispatches_publish_easyproxy(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            missing_config = Path(temp_dir) / "missing-config.yaml"

            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "deploy-subproject.ps1"),
                    "-Project",
                    "publish-easyproxy-image",
                    "-ConfigPath",
                    str(missing_config),
                    "-GhcrOwner",
                    "test-owner",
                    "-ReleaseTag",
                    "smoke-tag",
                    "-LoadOnly",
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 1)
            record = records[0]
            self.assertEqual(record["FilePath"], "powershell")
            args = record["Arguments"]
            self.assertIn("publish-ghcr-images.ps1", " ".join(args))
            self.assertIn("-Target", args)
            self.assertIn("easyproxy", args)
            self.assertIn("-ReleaseTag", args)
            self.assertIn("smoke-tag", args)

    def test_publish_ghcr_images_dispatches_both_images_in_capture_mode(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "ghcr.jsonl"
            missing_config = Path(temp_dir) / "missing-config.yaml"

            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "publish-ghcr-images.ps1"),
                    "-ConfigPath",
                    str(missing_config),
                    "-GhcrOwner",
                    "test-owner",
                    "-Target",
                    "both",
                    "-ReleaseTag",
                    "smoke-release",
                    "-LoadOnly",
                ],
                env={"EASYPROXY_TEST_CAPTURE_GHCR_BUILDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 2)

            images = {record["ImageName"]: record for record in records}
            self.assertIn("easy-proxy-monorepo-service", images)
            self.assertIn("ech-workers-monorepo", images)
            self.assertEqual(images["easy-proxy-monorepo-service"]["ReleaseTag"], "smoke-release")
            self.assertEqual(images["ech-workers-monorepo"]["ReleaseTag"], "smoke-release")
            self.assertTrue(images["easy-proxy-monorepo-service"]["LoadOnly"])
            self.assertTrue(images["ech-workers-monorepo"]["LoadOnly"])

    def test_publish_ghcr_images_rejects_placeholder_owner(self):
        result = self.run_powershell(
            [
                "-File",
                str(REPO_ROOT / "scripts" / "publish-ghcr-images.ps1"),
                "-ConfigPath",
                str(REPO_ROOT / "config.example.yaml"),
                "-GhcrOwner",
                "your-github-owner",
                "-Target",
                "easyproxy",
                "-ReleaseTag",
                "smoke-release",
                "-LoadOnly",
            ]
        )

        self.assertNotEqual(result.returncode, 0)
        combined = f"{result.stdout}\n{result.stderr}"
        self.assertIn("placeholder value", combined)


if __name__ == "__main__":
    unittest.main()
