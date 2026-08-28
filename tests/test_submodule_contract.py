import configparser
import re
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MODULES = {
    "upstreams/aggregator": "https://github.com/aiaimimi0920/aggregator.git",
    "upstreams/misub": "https://github.com/aiaimimi0920/MiSub.git",
    "upstreams/ech-workers": "https://github.com/aiaimimi0920/ech-workers.git",
}


def load_gitmodules(path: Path) -> configparser.ConfigParser:
    parser = configparser.ConfigParser()
    parser.read(path, encoding="utf-8")
    return parser


def run_git(*args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return result.stdout.strip()


class SubmoduleContractTests(unittest.TestCase):
    def test_root_pins_public_forks_as_gitlinks(self) -> None:
        modules = load_gitmodules(ROOT / ".gitmodules")

        for module_path, expected_url in MODULES.items():
            section = f'submodule "{module_path}"'
            self.assertTrue(modules.has_section(section), module_path)
            self.assertEqual(modules.get(section, "path"), module_path)
            self.assertEqual(modules.get(section, "url"), expected_url)

            index_entry = run_git("ls-files", "--stage", "--", module_path)
            self.assertTrue(index_entry.startswith("160000 "), index_entry)
            self.assertTrue((ROOT / module_path / "UPSTREAM.md").is_file())

    def test_aggregator_nested_manager_is_public(self) -> None:
        nested = load_gitmodules(ROOT / "upstreams/aggregator/.gitmodules")
        section = 'submodule "manager"'
        self.assertTrue(nested.has_section(section))
        self.assertEqual(nested.get(section, "path"), "manager")
        self.assertEqual(
            nested.get(section, "url"),
            "https://github.com/wzdnzd/proxy-manager.git",
        )

    def test_every_actions_checkout_is_recursive(self) -> None:
        checkout_count = 0
        recursive_count = 0
        workflows = [
            *sorted((ROOT / ".github/workflows").glob("*.yml")),
            *sorted((ROOT / ".github/workflows").glob("*.yaml")),
        ]
        for workflow in workflows:
            content = workflow.read_text(encoding="utf-8")
            checkout_count += len(re.findall(r"uses:\s*actions/checkout@", content))
            recursive_count += len(re.findall(r"submodules:\s*recursive", content))

        self.assertGreater(checkout_count, 0)
        self.assertEqual(recursive_count, checkout_count)

    def test_legacy_copy_sync_is_removed(self) -> None:
        self.assertFalse((ROOT / "scripts/sync-from-proxyservice.ps1").exists())


if __name__ == "__main__":
    unittest.main()
