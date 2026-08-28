import hashlib
import json
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path


ROOT = Path(__file__).parents[1]
PACKAGE = ROOT / "scripts/package-native-release.py"
MANIFEST = ROOT / "scripts/build-native-release-manifest.py"


class NativeReleaseTests(unittest.TestCase):
    def make_binary(self, directory: Path, name: str) -> Path:
        path = directory / name
        path.write_bytes((name + "-binary").encode())
        return path

    def package(self, root: Path, platform: str, arch: str) -> Path:
        output = root / "dist"
        command = [
            sys.executable, str(PACKAGE), "--platform", platform, "--arch", arch,
            "--version", "v-test", "--easy-proxy", str(self.make_binary(root, f"easy-{platform}-{arch}")),
            "--easyproxyctl", str(self.make_binary(root, f"ctl-{platform}-{arch}")),
            "--ech-workers", str(self.make_binary(root, f"ech-{platform}-{arch}")), "--output", str(output),
        ]
        result = subprocess.run(command, capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
        return output

    def test_packages_have_install_contract_and_are_deterministic(self):
        with tempfile.TemporaryDirectory() as first, tempfile.TemporaryDirectory() as second:
            first_dist = self.package(Path(first), "linux", "amd64")
            second_dist = self.package(Path(second), "linux", "amd64")
            archive = first_dist / "easyproxy-linux-amd64.tar.gz"
            self.assertEqual(hashlib.sha256(archive.read_bytes()).digest(), hashlib.sha256((second_dist / archive.name).read_bytes()).digest())
            with tarfile.open(archive) as package:
                names = package.getnames()
            self.assertIn("bin/easy-proxy", names)
            self.assertIn("bin/ech-workers", names)
            self.assertIn("config.example.yaml", names)
            self.assertIn("install/easyproxy.service", names)
            self.assertIn("VERSION", names)

    def test_windows_package_contains_real_service_installer(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            output = self.package(Path(temp_dir), "windows", "amd64")
            with zipfile.ZipFile(output / "easyproxy-windows-amd64.zip") as package:
                names = package.namelist()
            self.assertIn("bin/easy-proxy.exe", names)
            self.assertIn("install/install-service.ps1", names)

    def test_manifest_verification_detects_tampering(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            directory = Path(temp_dir)
            for name in (
                "easyproxy-linux-amd64.tar.gz", "easyproxy-linux-arm64.tar.gz", "easyproxy-windows-amd64.zip",
                "easyproxyctl-linux-amd64.tar.gz", "easyproxyctl-linux-arm64.tar.gz", "easyproxyctl-windows-amd64.zip",
                "config.example.yaml", "easyproxy.service", "install.sh", "install-service.ps1", "native-install-update.md",
            ):
                (directory / name).write_bytes(name.encode())
            build = subprocess.run([sys.executable, str(MANIFEST), "--directory", str(directory), "--version", "v-test", "--commit", "abc"], capture_output=True, text=True)
            self.assertEqual(build.returncode, 0, msg=build.stderr)
            payload = json.loads((directory / "release-manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(payload["unsupportedTargets"][0]["target"], "windows-arm64")
            (directory / "easyproxy-linux-arm64.tar.gz").write_bytes(b"tampered")
            verify = subprocess.run([sys.executable, str(MANIFEST), "--directory", str(directory), "--verify"], capture_output=True, text=True)
            self.assertNotEqual(verify.returncode, 0)
            self.assertIn("checksum mismatch", verify.stderr)

    def test_manifest_verification_rejects_extra_checksum_entry(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            directory = Path(temp_dir)
            required = (
                "easyproxy-linux-amd64.tar.gz", "easyproxy-linux-arm64.tar.gz", "easyproxy-windows-amd64.zip",
                "easyproxyctl-linux-amd64.tar.gz", "easyproxyctl-linux-arm64.tar.gz", "easyproxyctl-windows-amd64.zip",
                "config.example.yaml", "easyproxy.service", "install.sh", "install-service.ps1", "native-install-update.md",
            )
            for name in required:
                (directory / name).write_bytes(name.encode())
            subprocess.run([sys.executable, str(MANIFEST), "--directory", str(directory), "--version", "v-test", "--commit", "abc"], check=True)
            with (directory / "SHA256SUMS").open("a", encoding="utf-8") as handle:
                handle.write("0" * 64 + "  extra.bin\n")
            verify = subprocess.run([sys.executable, str(MANIFEST), "--directory", str(directory), "--verify"], capture_output=True, text=True)
            self.assertNotEqual(verify.returncode, 0)
            self.assertIn("artifact set mismatch", verify.stderr)


if __name__ == "__main__":
    unittest.main()
