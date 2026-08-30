import unittest

from scripts.configure_misub_pages_authority import (
    automatic_deployments_disabled,
    build_source_patch,
)


class MiSubPagesAuthorityTests(unittest.TestCase):
    def test_direct_upload_project_needs_no_source_patch(self):
        project = {"name": "misub", "source": None}

        self.assertTrue(automatic_deployments_disabled(project))
        self.assertIsNone(build_source_patch(project))

    def test_git_project_patch_disables_automatic_deployments(self):
        project = {
            "source": {
                "type": "github",
                "config": {
                    "owner": "operator",
                    "owner_id": "owner-id",
                    "repo_id": "repo-id",
                    "repo_name": "MiSub",
                    "production_branch": "main",
                    "path_includes": ["src/*"],
                    "pr_comments_enabled": True,
                    "production_deployments_enabled": True,
                    "preview_deployment_setting": "all",
                },
            }
        }

        patch = build_source_patch(project)

        self.assertEqual(patch["source"]["type"], "github")
        self.assertEqual(patch["source"]["config"]["repo_name"], "MiSub")
        self.assertEqual(patch["source"]["config"]["path_includes"], ["src/*"])
        self.assertTrue(patch["source"]["config"]["pr_comments_enabled"])
        self.assertFalse(patch["source"]["config"]["deployments_enabled"])
        self.assertFalse(patch["source"]["config"]["production_deployments_enabled"])
        self.assertEqual(patch["source"]["config"]["preview_deployment_setting"], "none")

    def test_disabled_git_project_is_recognized(self):
        project = {
            "source": {
                "type": "github",
                "config": {
                    "production_deployments_enabled": False,
                    "preview_deployment_setting": "none",
                },
            }
        }

        self.assertTrue(automatic_deployments_disabled(project))


if __name__ == "__main__":
    unittest.main()
