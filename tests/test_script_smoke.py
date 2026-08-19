import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[1]


def read_json_lines(path: Path):
    if not path.exists():
        return []
    lines = [line.strip() for line in path.read_text(encoding="utf-8-sig").splitlines() if line.strip()]
    return [json.loads(line) for line in lines]


class ScriptSmokeTests(unittest.TestCase):
    def root_config_fixture(self):
        return yaml.safe_load((REPO_ROOT / "config.example.yaml").read_text(encoding="utf-8")) or {}

    def run_renderer(self, root):
        with tempfile.TemporaryDirectory() as temp_dir:
            root_path = Path(temp_dir) / "config.yaml"
            output_path = Path(temp_dir) / "service-config.yaml"
            root_path.write_text(
                yaml.safe_dump(root, sort_keys=False, allow_unicode=True),
                encoding="utf-8",
            )
            result = subprocess.run(
                [
                    sys.executable,
                    str(REPO_ROOT / "scripts" / "render-derived-configs.py"),
                    "--root-config",
                    str(root_path),
                    "--service-output",
                    str(output_path),
                ],
                cwd=REPO_ROOT,
                capture_output=True,
                text=True,
                timeout=120,
            )
            rendered = yaml.safe_load(output_path.read_text(encoding="utf-8")) if output_path.exists() else None
            return result, rendered

    def render_service_runtime(self, root):
        result, rendered = self.run_renderer(root)
        self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
        return rendered or {}

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

    def test_root_routing_config_renders_into_service_config(self):
        root_config_path = REPO_ROOT / "config.example.yaml"
        root = yaml.safe_load(root_config_path.read_text(encoding="utf-8")) or {}
        runtime = root.get("serviceBase", {}).get("runtime", {})
        self.assertIn("routing", runtime, "config.example.yaml must expose the canonical routing block")
        self.assertFalse(runtime["routing"].get("enabled", True))
        self.assertEqual(runtime["routing"].get("rule_files"), [])
        self.assertIn("local_server", runtime, "config.example.yaml must expose the canonical Local Server block")
        self.assertFalse(runtime["local_server"].get("enabled", True))
        self.assertIn("gateway", runtime, "config.example.yaml must expose the gateway block")
        self.assertEqual(runtime["gateway"].get("mode"), "transparent")
        self.assertEqual(runtime["gateway"]["tun"].get("interface_name"), "easyproxy0")

        with tempfile.TemporaryDirectory() as temp_dir:
            output = Path(temp_dir) / "service-config.yaml"
            result = subprocess.run(
                [
                    sys.executable,
                    str(REPO_ROOT / "scripts" / "render-derived-configs.py"),
                    "--root-config",
                    str(root_config_path),
                    "--service-output",
                    str(output),
                ],
                cwd=REPO_ROOT,
                capture_output=True,
                text=True,
                timeout=120,
            )
            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            rendered = yaml.safe_load(output.read_text(encoding="utf-8")) or {}
            self.assertEqual(rendered["routing"]["enabled"], False)
            self.assertEqual(rendered["routing"]["final_policy"], "PROXY")
            self.assertEqual(rendered["routing"]["rule_files"], [])
            self.assertIn("long_lived", rendered["routing"])
            self.assertEqual(rendered["gateway"]["tun"]["stack"], "mixed")

    def test_root_local_server_render_derives_canonical_credentials(self):
        root = self.root_config_fixture()
        runtime = root["serviceBase"]["runtime"]
        runtime["mode"] = "pool"
        runtime["listener"]["protocol"] = "mixed"
        runtime["local_server"] = {
            "enabled": True,
            "listen": "",
            "auth": {"username": "easyproxy", "password": "test-secret"},
        }

        rendered = self.render_service_runtime(root)

        self.assertTrue(rendered["local_server"]["enabled"])
        self.assertEqual(rendered["listener"]["username"], "easyproxy")
        self.assertEqual(rendered["listener"]["password"], "test-secret")
        self.assertEqual(rendered["management"]["password"], "test-secret")
        self.assertEqual(rendered["local_server"]["shared_revision"], 1)
        self.assertEqual(rendered["local_server"]["credential_generation"], 2)

    def test_root_local_server_render_rejects_bypass_topology(self):
        root = self.root_config_fixture()
        root["serviceBase"]["runtime"]["local_server"] = {
            "enabled": True,
            "listen": "",
            "auth": {"username": "easyproxy", "password": "test-secret"},
        }

        result, _ = self.run_renderer(root)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("mode: pool", result.stderr)

    def test_root_local_server_render_rejects_non_mixed_listener(self):
        root = self.root_config_fixture()
        runtime = root["serviceBase"]["runtime"]
        runtime["mode"] = "pool"
        runtime["listener"]["protocol"] = "http"
        runtime["local_server"] = {
            "enabled": True,
            "listen": "",
            "auth": {"username": "easyproxy", "password": "test-secret"},
        }

        result, _ = self.run_renderer(root)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("listener.protocol: mixed", result.stderr)

    def test_root_local_server_render_rejects_extra_listeners(self):
        root = self.root_config_fixture()
        runtime = root["serviceBase"]["runtime"]
        runtime["mode"] = "pool"
        runtime["listener"]["protocol"] = "mixed"
        runtime["extra_listeners"] = [{"address": "0.0.0.0", "port": 24000, "protocol": "http"}]
        runtime["local_server"] = {
            "enabled": True,
            "listen": "",
            "auth": {"username": "easyproxy", "password": "test-secret"},
        }

        result, _ = self.run_renderer(root)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not allow extra_listeners", result.stderr)

    def test_root_local_server_render_rejects_conflicting_listens(self):
        root = self.root_config_fixture()
        runtime = root["serviceBase"]["runtime"]
        runtime["mode"] = "pool"
        runtime["listener"]["protocol"] = "mixed"
        runtime["routing"]["listen"] = "0.0.0.0:22324"
        runtime["local_server"] = {
            "enabled": True,
            "listen": "0.0.0.0:22323",
            "auth": {"username": "easyproxy", "password": "test-secret"},
        }

        result, _ = self.run_renderer(root)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("local_server.listen conflicts with routing.listen", result.stderr)

    def test_root_local_server_render_rejects_invalid_username(self):
        root = self.root_config_fixture()
        runtime = root["serviceBase"]["runtime"]
        runtime["mode"] = "pool"
        runtime["listener"]["protocol"] = "mixed"
        runtime["local_server"] = {
            "enabled": True,
            "listen": "",
            "auth": {"username": "invalid user", "password": "test-secret"},
        }

        result, _ = self.run_renderer(root)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("canonical username is invalid", result.stderr)

    def test_root_local_server_render_rejects_unresolved_password(self):
        for password in ("", "change_me_to_a_strong_shared_password"):
            with self.subTest(password=password):
                root = self.root_config_fixture()
                runtime = root["serviceBase"]["runtime"]
                runtime["mode"] = "pool"
                runtime["listener"]["protocol"] = "mixed"
                runtime["local_server"] = {
                    "enabled": True,
                    "listen": "",
                    "auth": {"username": "easyproxy", "password": password},
                }

                result, _ = self.run_renderer(root)

                self.assertNotEqual(result.returncode, 0)
                self.assertIn("canonical credential is unresolved", result.stderr)

    def test_disabled_root_local_server_preserves_legacy_credentials(self):
        root = self.root_config_fixture()
        runtime = root["serviceBase"]["runtime"]
        runtime["listener"]["username"] = "legacy-user"
        runtime["listener"]["password"] = "legacy-proxy-password"
        runtime["management"]["password"] = "legacy-management-password"
        runtime["local_server"] = {
            "enabled": False,
            "listen": "",
            "auth": {
                "username": "easyproxy",
                "password": "change_me_to_a_strong_shared_password",
            },
        }

        rendered = self.render_service_runtime(root)

        self.assertEqual(rendered["listener"]["username"], "legacy-user")
        self.assertEqual(rendered["listener"]["password"], "legacy-proxy-password")
        self.assertEqual(rendered["management"]["password"], "legacy-management-password")

    def test_deploy_subproject_dispatches_publish_service_base_config(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            temp_config = Path(temp_dir) / "config.yaml"
            template = (REPO_ROOT / "config.example.yaml").read_text(encoding="utf-8")
            temp_config.write_text(
                template.replace("accountId: \"\"", 'accountId: "cf-account-id"'),
                encoding="utf-8",
            )
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "deploy-subproject.ps1"),
                    "-Project",
                    "publish-service-base-config",
                    "-ConfigPath",
                    str(temp_config),
                    "-ReleaseTag",
                    "config-tag",
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 1)
            record = records[0]
            self.assertEqual(record["FilePath"], "powershell")
            args = record["Arguments"]
            self.assertIn("publish-service-base-config.ps1", " ".join(args))
            self.assertIn("-ReleaseVersion", args)
            self.assertIn("config-tag", args)

    def test_deploy_subproject_dispatches_easyproxy_ghcr(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            temp_config = Path(temp_dir) / "config.yaml"
            template = (REPO_ROOT / "config.example.yaml").read_text(encoding="utf-8")
            temp_config.write_text(
                template.replace(
                    "https://misub.example.com/api/manifest/your-profile",
                    "https://misub.aiaimimi.com/api/manifest/aggregator-global",
                ),
                encoding="utf-8",
            )

            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "deploy-subproject.ps1"),
                    "-Project",
                    "easyproxy-ghcr",
                    "-ConfigPath",
                    str(temp_config),
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
            record = records[0]
            self.assertEqual(record["FilePath"], "powershell")
            args = record["Arguments"]
            self.assertIn("deploy-easyproxy.ps1", " ".join(args))
            self.assertIn("-FromGhcr", args)
            self.assertIn("-ReleaseTag", args)
            self.assertIn("smoke-release", args)
            self.assertIn("-GhcrOwner", args)
            self.assertIn("test-owner", args)
            self.assertIn("-SkipPull", args)

    def test_deploy_subproject_rejects_unresolved_local_server_password_before_dispatch(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            temp_config = Path(temp_dir) / "config.yaml"
            root = self.root_config_fixture()
            runtime = root["serviceBase"]["runtime"]
            runtime["mode"] = "pool"
            runtime["listener"]["protocol"] = "mixed"
            runtime["local_server"] = {
                "enabled": True,
                "listen": "",
                "auth": {
                    "username": "easyproxy",
                    "password": "change_me_to_a_strong_shared_password",
                },
            }
            temp_config.write_text(
                yaml.safe_dump(root, sort_keys=False, allow_unicode=True),
                encoding="utf-8",
            )

            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "deploy-subproject.ps1"),
                    "-Project",
                    "easyproxy",
                    "-ConfigPath",
                    str(temp_config),
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("local_server.auth.password must be a real shared secret", f"{result.stdout}\n{result.stderr}")
            self.assertEqual(read_json_lines(capture_path), [])

    def test_deploy_subproject_dispatches_sync_github_settings(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            temp_config = Path(temp_dir) / "config.yaml"
            template = (REPO_ROOT / "config.example.yaml").read_text(encoding="utf-8")
            temp_config.write_text(template, encoding="utf-8")

            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "deploy-subproject.ps1"),
                    "-Project",
                    "sync-github-settings",
                    "-ConfigPath",
                    str(temp_config),
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 1)
            record = records[0]
            self.assertEqual(record["FilePath"], "powershell")
            args = record["Arguments"]
            self.assertIn("sync-github-deployment-settings.ps1", " ".join(args))

    def test_deploy_easyproxy_dispatches_deploy_ghcr_runtime(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            temp_config = Path(temp_dir) / "config.yaml"
            rendered_config = Path(temp_dir) / "rendered-config.yaml"
            rendered_config.write_text("management:\n  password: \"\"\n", encoding="utf-8")
            template = (REPO_ROOT / "config.example.yaml").read_text(encoding="utf-8")
            template = template.replace(
                "https://misub.example.com/api/manifest/your-profile",
                "https://misub.aiaimimi.com/api/manifest/aggregator-global",
            )
            template = template.replace(
                "renderedConfigPath: deploy/service/base/config.yaml",
                f'renderedConfigPath: "{rendered_config.as_posix()}"',
            )
            temp_config.write_text(template, encoding="utf-8")

            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "deploy-easyproxy.ps1"),
                    "-ConfigPath",
                    str(temp_config),
                    "-FromGhcr",
                    "-ReleaseTag",
                    "smoke-release",
                    "-GhcrOwner",
                    "test-owner",
                    "-SkipRender",
                    "-SkipPull",
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 1)
            record = records[0]
            self.assertEqual(record["FilePath"], "powershell")
            args = record["Arguments"]
            self.assertIn("deploy-ghcr-runtime.ps1", " ".join(args))
            self.assertIn("-Image", args)
            self.assertIn("ghcr.io/test-owner/easy-proxy-monorepo-service:smoke-release", args)
            self.assertIn("-RuntimeRoot", args)
            self.assertIn("-NetworkName", args)
            self.assertIn("EasyAiMi", args)
            self.assertIn("-SkipPull", args)

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

    def test_deploy_aggregator_dispatches_native_workflow(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            capture_path = Path(temp_dir) / "external.jsonl"
            result = self.run_powershell(
                [
                    "-File",
                    str(REPO_ROOT / "scripts" / "deploy-aggregator.ps1"),
                    "-ConfigPath",
                    str(REPO_ROOT / "config.example.yaml"),
                    "-DeploymentMode",
                    "bootstrap",
                ],
                env={"EASYPROXY_TEST_CAPTURE_EXTERNAL_COMMANDS_PATH": str(capture_path)},
            )

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            records = read_json_lines(capture_path)
            self.assertEqual(len(records), 2)
            workflow_run = records[1]
            self.assertEqual(workflow_run["FilePath"], "gh")
            args = workflow_run["Arguments"]
            self.assertEqual(args[:3], ["workflow", "run", "deploy-aggregator.yml"])
            self.assertIn("deployment_mode=bootstrap", args)
            self.assertIn("run_verification=true", args)


if __name__ == "__main__":
    unittest.main()
