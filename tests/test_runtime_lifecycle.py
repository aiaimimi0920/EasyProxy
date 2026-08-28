import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
LIFECYCLE = ROOT / "deploy/service/base/scripts/runtime-lifecycle.ps1"


class RuntimeLifecycleTests(unittest.TestCase):
    def run_powershell(self, command: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["powershell", "-NoProfile", "-Command", command],
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            check=False,
        )

    def test_backup_restore_preserves_config_and_sqlite(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            data = root / "data"
            data.mkdir()
            (root / "config.yaml").write_text("sentinel: original\n", encoding="utf-8")
            (root / ".env").write_text("EASY_PROXY_SERVICE_IMAGE=old\n", encoding="utf-8")
            (data / "data.db").write_bytes(b"sqlite-original")
            command = (
                f". '{LIFECYCLE}'; "
                f"$backup=New-EasyProxyRuntimeBackup -RuntimeRoot '{root}' -PreviousImage 'old'; "
                f"Set-Content -LiteralPath '{root / 'config.yaml'}' -Value 'changed'; "
                f"[IO.File]::WriteAllBytes('{data / 'data.db'}',[byte[]](1,2,3)); "
                f"Restore-EasyProxyRuntimeBackup -RuntimeRoot '{root}' -BackupPath $backup | Out-Null"
            )
            result = self.run_powershell(command)
            self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
            self.assertEqual((root / "config.yaml").read_text(encoding="utf-8"), "sentinel: original\n")
            self.assertEqual((data / "data.db").read_bytes(), b"sqlite-original")

    def test_restore_rejects_backup_outside_runtime_root(self):
        with tempfile.TemporaryDirectory() as temp_dir, tempfile.TemporaryDirectory() as outside:
            root = Path(temp_dir)
            command = (
                f". '{LIFECYCLE}'; "
                f"Restore-EasyProxyRuntimeBackup -RuntimeRoot '{root}' -BackupPath '{outside}'"
            )
            result = self.run_powershell(command)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("escapes EasyProxy runtime root", result.stderr)


if __name__ == "__main__":
    unittest.main()
