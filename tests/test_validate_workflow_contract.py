import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "validate.yml"


class ValidateWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")

    def test_root_ci_checks_go_format_and_vet(self):
        self.assertIn("python scripts/check-go-format.py", self.workflow)
        self.assertIn("go vet ./...", self.workflow)

    def test_root_ci_rejects_uncommitted_generated_assets(self):
        self.assertIn("git diff --exit-code", self.workflow)


if __name__ == "__main__":
    unittest.main()
