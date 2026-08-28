import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "validate.yml"
REUSABLE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "reusable-validate.yml"
CLOUDFLARE_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "deploy-cloudflare.yml"


class ValidateWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.reusable_workflow = REUSABLE_WORKFLOW.read_text(encoding="utf-8")
        cls.cloudflare_workflow = CLOUDFLARE_WORKFLOW.read_text(encoding="utf-8")

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


if __name__ == "__main__":
    unittest.main()
