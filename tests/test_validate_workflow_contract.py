import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "validate.yml"
REUSABLE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "reusable-validate.yml"
CLOUDFLARE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-cloudflare.yml"
BACKUP_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "backup-misub.yml"
RESTORE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "restore-misub.yml"
ROTATE_ECH_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "rotate-ech-token.yml"
AGGREGATOR_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-aggregator.yml"
PUBLISH_GHCR_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "publish-ghcr-images.yml"


class ValidateWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.reusable_workflow = REUSABLE_WORKFLOW.read_text(encoding="utf-8")
        cls.cloudflare_workflow = CLOUDFLARE_WORKFLOW.read_text(encoding="utf-8")
        cls.backup_workflow = BACKUP_WORKFLOW.read_text(encoding="utf-8")
        cls.restore_workflow = RESTORE_WORKFLOW.read_text(encoding="utf-8")
        cls.rotate_ech_workflow = ROTATE_ECH_WORKFLOW.read_text(encoding="utf-8")
        cls.aggregator_workflow = AGGREGATOR_WORKFLOW.read_text(encoding="utf-8")
        cls.publish_ghcr_workflow = PUBLISH_GHCR_WORKFLOW.read_text(encoding="utf-8")

    def test_root_ci_checks_go_format_and_vet(self):
        self.assertIn("uses: ./.github/workflows/reusable-validate.yml", self.workflow)
        self.assertIn("python -m pip install PyYAML tqdm requests boto3", self.reusable_workflow)
        self.assertIn("python scripts/check-go-format.py", self.reusable_workflow)
        self.assertIn("go vet ./...", self.reusable_workflow)
        self.assertIn("topology validate", self.reusable_workflow)

    def test_aggregator_preflight_installs_root_test_dependencies(self):
        self.assertIn("python -m pip install PyYAML tqdm requests boto3", self.aggregator_workflow)

    def test_other_release_preflights_install_root_test_dependencies(self):
        self.assertIn("websockets boto3", self.cloudflare_workflow)
        self.assertIn("python -m pip install PyYAML tqdm requests boto3", self.publish_ghcr_workflow)

    def test_root_ci_rejects_uncommitted_generated_assets(self):
        self.assertIn("git diff --exit-code", self.reusable_workflow)
        self.assertNotIn("secrets: inherit", self.workflow)

    def test_cloudflare_deploy_attaches_ech_to_runtime_profile(self):
        self.assertIn('--attach-profile-id "${runtime_profile_id}"', self.cloudflare_workflow)

    def test_cloudflare_preflight_uses_highest_required_go_version(self):
        self.assertIn("go-version-file: upstreams/ech-workers/go.mod", self.cloudflare_workflow)

    def test_each_wrangler_action_receives_the_cloudflare_token_input(self):
        action = "uses: cloudflare/wrangler-action@v3"
        token_input = "apiToken: ${{ secrets.CLOUDFLARE_API_TOKEN }}"
        self.assertGreater(self.cloudflare_workflow.count(action), 0)
        self.assertEqual(
            self.cloudflare_workflow.count(action),
            self.cloudflare_workflow.count(token_input),
        )

    def test_profile_sync_requires_the_selected_deployment_to_succeed(self):
        self.assertIn(
            "inputs.target == 'misub-pages' && needs.deploy-misub-pages.result == 'success'",
            self.cloudflare_workflow,
        )
        self.assertNotIn(
            "needs.deploy-misub-pages.result == 'success' || needs.deploy-misub-pages.result == 'skipped'",
            self.cloudflare_workflow,
        )

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

    def test_lifecycle_workflows_build_easyproxyctl_entrypoint(self):
        for workflow in (
            self.cloudflare_workflow,
            self.backup_workflow,
            self.restore_workflow,
        ):
            self.assertIn("go build -o", workflow)
            self.assertIn("./cmd/easyproxyctl", workflow)

    def test_restore_defaults_to_drill_and_keeps_durable_cleanup_identity(self):
        self.assertIn("default: drill", self.restore_workflow)
        self.assertIn("DRILL_TARGET_NAME", self.restore_workflow)
        self.assertIn("Refusing ambiguous drill cleanup", self.restore_workflow)
        self.assertIn("environment: easyproxy-misub-restore", self.restore_workflow)

    def test_ordinary_ech_update_preserves_the_canonical_token(self):
        self.assertIn("secrets.ECH_TOKEN", self.cloudflare_workflow)
        self.assertNotIn("ECH_TOKEN_NEXT", self.cloudflare_workflow)
        self.assertIn("verify-cloudflare-worker-identity.py", self.cloudflare_workflow)
        self.assertIn("Prove update is not an implicit token rotation", self.cloudflare_workflow)

    def test_ech_job_installs_verification_dependencies_before_identity_check(self):
        ech_job = self.cloudflare_workflow.split("  deploy-ech-workers-cloudflare:", 1)[1]
        dependency_step = ech_job.index("Install ECH verification dependencies")
        identity_step = ech_job.index("Resolve exact Worker identity")
        self.assertLess(dependency_step, identity_step)
        self.assertIn("python -m pip install requests websockets", ech_job[:identity_step])

    def test_ech_rotation_has_overlap_candidate_revocation_and_rollback(self):
        workflow = self.rotate_ech_workflow
        self.assertIn("environment: easyproxy-ech-rotation", workflow)
        self.assertIn("ECH_TOKEN_PREVIOUS_EXPIRES_AT", workflow)
        self.assertIn("--previous-token accepted", workflow)
        self.assertIn("--candidate-easyproxy-base-url", workflow)
        self.assertIn("Persist the new canonical repository secret", workflow)
        self.assertIn("--previous-token rejected", workflow)
        self.assertIn("Restore the old token and connector after any failure", workflow)

    def test_ech_rotation_does_not_put_tokens_in_process_arguments(self):
        self.assertNotIn("--token", self.rotate_ech_workflow)


if __name__ == "__main__":
    unittest.main()
