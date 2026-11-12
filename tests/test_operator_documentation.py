import re
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


class OperatorDocumentationContractTests(unittest.TestCase):
    def test_cloudflare_account_id_uses_repository_variable(self):
        workflows = "\n".join(
            path.read_text(encoding="utf-8")
            for path in (REPO_ROOT / ".github/workflows").glob("*.yml")
        )

        self.assertNotIn("secrets.CLOUDFLARE_ACCOUNT_ID", workflows)
        self.assertIn("vars.CLOUDFLARE_ACCOUNT_ID", workflows)

    def test_cloudflare_workflows_reject_global_api_keys(self):
        workflows = "\n".join(
            path.read_text(encoding="utf-8")
            for path in (REPO_ROOT / ".github/workflows").glob("*.yml")
        )

        self.assertNotIn("CLOUDFLARE_GLOBAL_API_KEY", workflows)
        self.assertNotIn("CLOUDFLARE_AUTH_EMAIL", workflows)

    def test_every_workflow_setting_is_named_in_public_operator_docs(self):
        workflows = "\n".join(
            path.read_text(encoding="utf-8")
            for path in (REPO_ROOT / ".github/workflows").glob("*.yml")
        )
        operator_docs = "\n".join(
            (REPO_ROOT / "docs" / name).read_text(encoding="utf-8")
            for name in ("security.md", "deployment.md")
        )

        for context in ("secrets", "vars"):
            names = set(
                re.findall(rf"\$\{{\{{\s*{context}\.([A-Z0-9_]+)", workflows)
            )
            missing = sorted(name for name in names if name not in operator_docs)
            self.assertEqual([], missing, f"undocumented {context}: {missing}")

    def test_public_docs_do_not_link_to_local_windows_paths(self):
        public_docs = [
            REPO_ROOT / "README.md",
            *(REPO_ROOT / "docs").glob("*.md"),
            *(REPO_ROOT / "deploy").glob("**/README.md"),
        ]
        markdown = "\n".join(
            path.read_text(encoding="utf-8")
            for path in public_docs
        )

        self.assertNotIn("/C:/Users/", markdown)

    def test_consolidated_topic_documents_and_local_links_exist(self):
        topics = (
            "architecture.md", "security.md", "release.md", "development.md",
            "routing.md", "local-server.md", "deployment-records.md",
            "cloud-operations.md", "deployment.md",
        )
        paths = [REPO_ROOT / "docs" / name for name in topics]
        for path in paths:
            self.assertTrue(path.is_file(), str(path))
        for path in [REPO_ROOT / "README.md", *paths]:
            for target in re.findall(r"\]\(([^\s)]+)\)", path.read_text(encoding="utf-8")):
                if "://" in target or target.startswith("#"):
                    continue
                relative = target.split("#", 1)[0]
                if relative:
                    self.assertTrue((path.parent / relative).exists(), f"{path}: {target}")

    def test_development_gate_limits_match_checker(self):
        guide = (REPO_ROOT / "docs/development.md").read_text(encoding="utf-8")
        checker = (REPO_ROOT / "scripts/check-effective-lines.py").read_text(encoding="utf-8")
        hard_limit = re.search(r"^HARD_LIMIT = (\d+)$", checker, re.MULTILINE).group(1)
        self.assertIn(f"| >{hard_limit} | 当前 checker 硬上限违规", guide)
        self.assertIn("INCLUDED_PREFIXES", guide)
        self.assertIn("easyproxy-runtime.yaml", guide)
        self.assertIn("deploy/service/base/config.yaml", guide)

    def test_topic_document_format_is_consistent(self):
        for path in (REPO_ROOT / "docs").glob("*.md"):
            self.assertRegex(path.name, r"^[a-z]+(?:-[a-z]+)*\.md$")
            text = path.read_text(encoding="utf-8")
            self.assertIn("## 适用范围", text)
            self.assertIn("## 目录", text)
            fenced = False
            headings = []
            for line in text.splitlines():
                if line.startswith("```"):
                    fenced = not fenced
                elif not fenced and line.startswith("# "):
                    headings.append(line)
            self.assertEqual(1, len(headings), str(path))
            self.assertTrue(headings[0].startswith("# EasyProxy "))

    def test_readme_screenshots_are_not_shipped_as_runtime_assets(self):
        service = REPO_ROOT / "service" / "base"
        readme = (service / "README.md").read_text(encoding="utf-8")
        for number in range(1, 6):
            name = f"{number}.png"
            self.assertIn(f"./docs/screenshots/{name}", readme)
            self.assertTrue((service / "docs" / "screenshots" / name).is_file())
            self.assertFalse((service / "frontend" / "public" / name).exists())
            self.assertFalse((service / "internal" / "monitor" / "assets" / name).exists())

    def test_native_installers_require_fork_neutral_release_source(self):
        linux = (REPO_ROOT / "deploy/native/linux/install.sh").read_text(encoding="utf-8")
        windows = (REPO_ROOT / "deploy/native/windows/install-service.ps1").read_text(encoding="utf-8")

        self.assertIn("--repository", linux)
        self.assertIn("EASYPROXY_RELEASE_REPOSITORY", linux)
        self.assertIn("-Repository", windows)
        self.assertIn("EASYPROXY_RELEASE_REPOSITORY", windows)
        self.assertNotIn("aiaimimi0920/EasyProxy/releases", linux)
        self.assertNotIn("aiaimimi0920/EasyProxy/releases", windows)

    def test_fork_guide_names_protected_lifecycle_workflows(self):
        guide = (REPO_ROOT / "docs/deployment.md").read_text(encoding="utf-8")

        for required in (
            "Deploy Cloudflare Apps",
            "Backup MiSub",
            "Restore MiSub",
            "Rotate ECH Token",
            "Deploy Aggregator",
            "Publish GHCR Images",
            "Publish GitHub Release",
        ):
            self.assertIn(required, guide)
        self.assertIn("<OWNER>/<REPOSITORY>", guide)
        self.assertIn("required reviewers for every restore run", guide)
        self.assertIn("EASYPROXY_MISUB_CONNECTOR_PROFILE_ID", guide)


if __name__ == "__main__":
    unittest.main()
