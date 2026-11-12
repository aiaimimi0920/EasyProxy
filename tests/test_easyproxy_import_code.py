import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]


class EasyProxyImportCodeTests(unittest.TestCase):
    def run_python(self, *args):
        return subprocess.run(
            [sys.executable, str(REPO_ROOT / "scripts" / "easyproxy-import-code.py"), *args],
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

    def test_keypair_encrypt_decrypt_file_round_trip(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            public, private = root / "public.txt", root / "private.txt"
            bundle, encrypted, restored = root / "bundle.json", root / "encrypted.json", root / "restored.txt"
            bundle.write_text(json.dumps({"importCode": "test-import-code", "payload": {"test": True}}))
            commands = (
                ("generate-keypair", "--public-key-output", str(public), "--private-key-output", str(private)),
                ("encrypt", "--bundle-file", str(bundle), "--public-key-file", str(public), "--output", str(encrypted)),
                ("decrypt", "--encrypted-file", str(encrypted), "--private-key-file", str(private), "--import-code-only", "--output", str(restored)),
            )
            for command in commands:
                result = self.run_python(*command)
                self.assertEqual(0, result.returncode, result.stderr)
                self.assertEqual("", result.stdout)
            self.assertEqual("test-import-code", restored.read_text().strip())


if __name__ == "__main__":
    unittest.main()
