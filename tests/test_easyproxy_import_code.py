import json
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


class EasyProxyImportCodeTests(unittest.TestCase):
    def run_python(self, *args):
        return subprocess.run(
            ["python", str(REPO_ROOT / "scripts" / "easyproxy-import-code.py"), *args],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=30,
        )

    def test_encode_and_inspect_round_trip(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            bundle_path = Path(temp_dir) / "bundle.json"
            inspect_path = Path(temp_dir) / "inspect.json"

            result = self.run_python(
                "encode",
                "--account-id", "account-id",
                "--bucket", "bucket-name",
                "--manifest-object-key", "service-base/manifest.json",
                "--access-key-id", "read-key",
                "--secret-access-key", "read-secret",
                "--release-version", "release-20260428-001",
                "--json-output",
                "--output", str(bundle_path),
            )
            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)

            bundle = json.loads(bundle_path.read_text(encoding="utf-8"))
            import_code = bundle["importCode"]
            self.assertTrue(import_code.startswith("easyproxy-import-v1."))

            inspect = self.run_python(
                "inspect",
                "--import-code", import_code,
                "--output", str(inspect_path),
            )
            self.assertEqual(inspect.returncode, 0, msg=inspect.stderr or inspect.stdout)

            payload = json.loads(inspect_path.read_text(encoding="utf-8"))
            self.assertEqual(payload["accountId"], "account-id")
            self.assertEqual(payload["bucket"], "bucket-name")
            self.assertEqual(payload["manifestObjectKey"], "service-base/manifest.json")
            self.assertEqual(payload["releaseVersion"], "release-20260428-001")


if __name__ == "__main__":
    unittest.main()
