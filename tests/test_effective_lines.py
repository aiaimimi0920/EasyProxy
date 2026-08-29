import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path, PurePosixPath


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "check-effective-lines.py"
SPEC = importlib.util.spec_from_file_location("check_effective_lines", SCRIPT)
CHECKER = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = CHECKER
SPEC.loader.exec_module(CHECKER)


class EffectiveLineCounterTests(unittest.TestCase):
    def test_ignores_blank_and_comment_only_lines(self):
        source = """
// comment
package sample
/* block
comment */
func value() int { // inline comment
    return 1
}
"""

        count = CHECKER.count_effective_lines(source, PurePosixPath("sample.go"))

        self.assertEqual(count, 4)

    def test_counts_comment_markers_inside_strings(self):
        source = 'const url = "https://example.test/path";\nconst marker = "/* value */";\n'

        count = CHECKER.count_effective_lines(source, PurePosixPath("sample.ts"))

        self.assertEqual(count, 2)

    def test_counts_multiline_raw_string_without_treating_markers_as_comments(self):
        source = "package sample\nvar script = `\n/* executable template */\nvalue // retained\n`\n"

        count = CHECKER.count_effective_lines(source, PurePosixPath("sample.go"))

        self.assertEqual(count, 5)


class EffectiveLineGateTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.repo = Path(self.temp_dir.name)
        subprocess.run(["git", "init", "-q"], cwd=self.repo, check=True)
        (self.repo / "service/base/internal/sample").mkdir(parents=True)

    def tearDown(self):
        self.temp_dir.cleanup()

    def write_baseline(self, entries=None):
        baseline = self.repo / "effective-lines-baseline.json"
        baseline.write_text(
            json.dumps(
                {
                    "schema_version": 1,
                    "soft_limit": 500,
                    "hard_limit": 700,
                    "legacy_over_limit": entries or {},
                }
            ),
            encoding="utf-8",
        )
        return baseline

    def write_tracked_source(self, line_count):
        source = self.repo / "service/base/internal/sample/sample.go"
        source.write_text("package sample\n" + "var _ = 1\n" * (line_count - 1), encoding="utf-8")
        subprocess.run(["git", "add", "."], cwd=self.repo, check=True)
        return source

    def test_rejects_new_hard_limit_violation(self):
        self.write_tracked_source(701)

        result = CHECKER.inspect_repository(self.repo, self.write_baseline())

        self.assertEqual(result, 1)

    def test_accepts_ratcheted_legacy_violation(self):
        self.write_tracked_source(701)
        entries = {
            "service/base/internal/sample/sample.go": {
                "max_effective_lines": 701,
                "reason": "legacy debt",
                "plan_wave": "R1",
            }
        }

        result = CHECKER.inspect_repository(self.repo, self.write_baseline(entries))

        self.assertEqual(result, 0)

    def test_rejects_grown_or_stale_legacy_exception(self):
        self.write_tracked_source(702)
        entries = {
            "service/base/internal/sample/sample.go": {
                "max_effective_lines": 701,
                "reason": "legacy debt",
                "plan_wave": "R1",
            }
        }
        baseline = self.write_baseline(entries)

        self.assertEqual(CHECKER.inspect_repository(self.repo, baseline), 1)
        source = self.repo / "service/base/internal/sample/sample.go"
        source.write_text("package sample\n", encoding="utf-8")
        self.assertEqual(CHECKER.inspect_repository(self.repo, baseline), 1)


if __name__ == "__main__":
    unittest.main()
