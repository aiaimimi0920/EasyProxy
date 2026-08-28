import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "validate.yml"
REUSABLE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "reusable-validate.yml"
CLOUDFLARE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-cloudflare.yml"
BACKUP_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "backup-misub.yml"
RESTORE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "restore-misub.yml"


class ValidateWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.reusable_workflow = REUSABLE_WORKFLOW.read_text(encoding="utf-8")
        cls.cloudflare_workflow = CLOUDFLARE_WORKFLOW.read_text(encoding="utf-8")
        cls.backup_workflow = BACKUP_WORKFLOW.read_text(encoding="utf-8")
        cls.restore_workflow = RESTORE_WORKFLOW.read_text(encoding="utf-8")

    def test_root_ci_checks_go_format_and_vet(self):
        self.assertIn("uses: ./.github/workflows/reusable-validate.yml", self.workflow)
        self.assertIn("python scripts/check-go-format.py", self.reusable_workflow)
        self.assertIn("go vet ./...", self.reusable_workflow)
        self.assertIn("topology validate", self.reusable_workflow)

    def test_root_ci_rejects_uncommitted_generated_assets(self):
        self.assertIn("git diff --exit-code", self.reusable_workflow)
        self.assertNotIn("secrets: inherit", self.workflow)

    def test_cloudflare_deploy_attaches_ech_to_runtime_profile(self):
        self.assertIn('--attach-profile-id "${runtime_profile_id}"', self.cloudflare_workflow)

    def test_misub_update_backs_up_before_migration_and_deploy(self):
        backup = self.cloudflare_workflow.index("cloud backup")
        retained = self.cloudflare_workflow.index("Retain encrypted pre-update backup")
        migration = self.cloudflare_workflow.index("d1 migrations apply")
        deploy = self.cloudflare_workflow.index("pages deploy dist")
        self.assertLess(backup, retained)
        self.assertLess(retained, migration)
        self.assertLess(migration, deploy)

    def test_cloudflare_workflow_does_not_pass_runtime_secrets_in_argv(self):
        workflows = self.cloudflare_workflow + self.backup_workflow + self.restore_workflow
        for option in ("--admin-password", "--manifest-token", "--access-token", "--token"):
            self.assertNotIn(option, workflows)

    def test_misub_resources_use_strict_lifecycle_cli(self):
        self.assertIn('easyproxyctl" cloud "$mode"', self.cloudflare_workflow)
        self.assertIn('easyproxyctl" cloud verify', self.cloudflare_workflow)
        self.assertNotIn("continuing in case the project already exists", self.cloudflare_workflow)

    def test_restore_defaults_to_drill_and_keeps_durable_cleanup_identity(self):
        self.assertIn("default: drill", self.restore_workflow)
        self.assertIn("DRILL_TARGET_NAME", self.restore_workflow)
        self.assertIn("Refusing ambiguous drill cleanup", self.restore_workflow)
        self.assertIn("environment: easyproxy-misub-restore", self.restore_workflow)


if __name__ == "__main__":
    unittest.main()
