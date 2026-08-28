import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[1]
TOPOLOGY_EXAMPLE = REPO_ROOT / "topology.example.yaml"


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

    def test_topology_and_runtime_templates_have_separate_authority(self):
        topology = yaml.safe_load(TOPOLOGY_EXAMPLE.read_text(encoding="utf-8"))
        runtime = yaml.safe_load(
            (REPO_ROOT / "deploy/service/base/config.template.yaml").read_text(encoding="utf-8")
        )

        self.assertIn("components", topology)
        self.assertIn("cloudflare", topology)
        self.assertIn("secrets", topology)
        self.assertNotIn("listener", topology)
        self.assertIn("listener", runtime)
        self.assertIn("routing", runtime)
        self.assertIn("local_server", runtime)
        self.assertIn("gateway", runtime)
        for reference in topology["secrets"].values():
            if reference:
                self.assertRegex(reference, r"^[A-Z][A-Z0-9_]*$")

    def test_initializers_refuse_implicit_overwrite(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            topology_path = Path(temp_dir) / "nested" / "topology.yaml"
            runtime_path = Path(temp_dir) / "runtime.yaml"
            topology_init = REPO_ROOT / "scripts/init-topology.ps1"
            runtime_init = REPO_ROOT / "scripts/init-runtime-config.ps1"

            first = self.run_powershell(["-File", str(topology_init), "-OutputPath", str(topology_path)])
            second = self.run_powershell(["-File", str(topology_init), "-OutputPath", str(topology_path)])
            runtime = self.run_powershell(["-File", str(runtime_init), "-OutputPath", str(runtime_path)])

            self.assertEqual(first.returncode, 0, msg=first.stderr or first.stdout)
            self.assertNotEqual(second.returncode, 0)
            self.assertIn("already exists", f"{second.stdout}\n{second.stderr}")
            self.assertEqual(runtime.returncode, 0, msg=runtime.stderr or runtime.stdout)
            self.assertIn("management", yaml.safe_load(runtime_path.read_text(encoding="utf-8")))

    def test_deploy_subproject_dispatches_publish_easyproxy(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts/deploy-subproject.ps1"),
                    "-Project",
                    "publish-easyproxy-image",
                    "-TopologyPath",
                    str(TOPOLOGY_EXAMPLE),
                    "-GhcrOwner",
                    "test-owner",
                    "-ReleaseTag",
                    "smoke-tag",
                    "-LoadOnly",
                ],
                env={
                    "EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path),
                    "GHCR_TOKEN": "must-not-enter-argv",
                },
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 1)
            args = records[0]["Arguments"]
            self.assertIn("publish-ghcr-images.ps1", " ".join(args))
            self.assertIn("easyproxy", args)
            self.assertIn("smoke-tag", args)
            self.assertNotIn("must-not-enter-argv", capture_path.read_text(encoding="utf-8-sig"))

    def test_deploy_subproject_dispatches_easyproxy_ghcr(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            runtime_path = Path(temp_dir) / "runtime.yaml"
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts/deploy-subproject.ps1"),
                    "-Project",
                    "easyproxy-ghcr",
                    "-TopologyPath",
                    str(TOPOLOGY_EXAMPLE),
                    "-RuntimeConfigPath",
                    str(runtime_path),
                    "-ReleaseTag",
                    "smoke-release",
                    "-GhcrOwner",
                    "test-owner",
                    "-SkipPull",
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 1)
            args = records[0]["Arguments"]
            self.assertIn("deploy-easyproxy.ps1", " ".join(args))
            self.assertIn("-FromGhcr", args)
            self.assertIn("-RuntimeConfigPath", args)
            self.assertIn("-SkipPull", args)

    def test_deploy_easyproxy_initializes_runtime_once_and_dispatches_ghcr(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            runtime_path = Path(temp_dir) / "runtime.yaml"
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts/deploy-easyproxy.ps1"),
                    "-TopologyPath",
                    str(TOPOLOGY_EXAMPLE),
                    "-RuntimeConfigPath",
                    str(runtime_path),
                    "-FromGhcr",
                    "-ReleaseTag",
                    "smoke-release",
                    "-GhcrOwner",
                    "test-owner",
                    "-SkipPull",
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            self.assertTrue(runtime_path.exists())
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 1)
            args = records[0]["Arguments"]
            self.assertIn("deploy-ghcr-runtime.ps1", " ".join(args))
            self.assertIn("ghcr.io/test-owner/easy-proxy-monorepo-service:smoke-release", args)
            self.assertIn("easyproxy-network", args)

    def test_publish_ghcr_images_dispatches_both_images_in_capture_mode(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "ghcr.jsonl"
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts/publish-ghcr-images.ps1"),
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
            self.assertEqual({record["ImageName"] for record in records}, {
                "easy-proxy-monorepo-service",
                "ech-workers-monorepo",
            })
            self.assertTrue(all(record["LoadOnly"] for record in records))
            self.assertNotIn("GhcrToken", capture_path.read_text(encoding="utf-8-sig"))

    def test_publish_ghcr_images_rejects_placeholder_owner(self):
        result = self.run_powershell(
            [
                "-File",
                str(REPO_ROOT / "scripts/publish-ghcr-images.ps1"),
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
        self.assertIn("placeholder value", f"{result.stdout}\n{result.stderr}")

    def test_deploy_aggregator_dispatches_native_workflow(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts/deploy-aggregator.ps1"),
                    "-TopologyPath",
                    str(TOPOLOGY_EXAMPLE),
                    "-DeploymentMode",
                    "bootstrap",
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 2)
            args = records[1]["Arguments"]
            self.assertEqual(args[:3], ["workflow", "run", "deploy-aggregator.yml"])
            self.assertIn("deployment_mode=bootstrap", args)

    def test_deploy_misub_uses_locked_dependency_install(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts/deploy-misub.ps1"),
                    "-TopologyPath",
                    str(TOPOLOGY_EXAMPLE),
                    "-Mode",
                    "pages",
                ],
                env={
                    "EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path),
                    "CLOUDFLARE_ACCOUNT_ID": "test-account",
                    "CLOUDFLARE_API_TOKEN": "test-token",
                },
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 3)
            self.assertEqual(records[0]["Arguments"], ["ci"])
            self.assertEqual(records[1]["Arguments"], ["run", "build"])
            self.assertIn("easyproxy-misub-pages", records[2]["Arguments"])

    def test_legacy_root_config_entrypoints_are_removed(self):
        removed = [
            "config.example.yaml",
            "scripts/init-config.ps1",
            "scripts/render-derived-configs.ps1",
            "scripts/render-derived-configs.py",
            "scripts/sync-github-deployment-settings.ps1",
            "scripts/sync-github-deployment-settings.py",
            "scripts/lib/easyproxy-config.ps1",
        ]
        self.assertEqual([path for path in removed if (REPO_ROOT / path).exists()], [])


if __name__ == "__main__":
    unittest.main()
