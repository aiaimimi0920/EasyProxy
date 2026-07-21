import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "check-go-format.py"


class CheckGoFormatTests(unittest.TestCase):
    def run_checker(self, path):
        return subprocess.run(
            [sys.executable, str(SCRIPT), str(path)],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=120,
        )

    def test_accepts_logically_formatted_crlf_source(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            source = Path(temp_dir) / "formatted.go"
            source.write_bytes(b"package sample\r\n\r\nfunc Value() int {\r\n\treturn 1\r\n}\r\n")

            result = self.run_checker(source)

            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            self.assertIn("1 Go files are gofmt-clean", result.stdout)

    def test_rejects_truly_unformatted_source(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            source = Path(temp_dir) / "unformatted.go"
            source.write_text("package sample\nfunc Value( )int{return 1}\n", encoding="utf-8")

            result = self.run_checker(source)

            self.assertNotEqual(result.returncode, 0)
            reported_path = Path(result.stdout.strip().splitlines()[-1])
            self.assertTrue(os.path.samefile(source, reported_path), msg=result.stdout)


if __name__ == "__main__":
    unittest.main()
