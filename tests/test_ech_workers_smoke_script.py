import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "deploy/upstreams/ech-workers/scripts/smoke-ech-workers-docker.ps1"


@unittest.skipUnless(shutil.which("powershell"), "Windows PowerShell is required")
class EchWorkersSmokeScriptTests(unittest.TestCase):
    def test_successful_native_stderr_help_is_accepted(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            fake_docker = Path(temp_dir) / "docker.cmd"
            fake_docker.write_text(
                "@echo off\r\n"
                "echo Usage of /usr/local/bin/ech-workers: 1^>^&2\r\n"
                "exit /b 0\r\n",
                encoding="ascii",
            )
            env = os.environ.copy()
            env["PATH"] = temp_dir + os.pathsep + env.get("PATH", "")

            result = subprocess.run(
                [
                    "powershell",
                    "-NoProfile",
                    "-ExecutionPolicy",
                    "Bypass",
                    "-File",
                    str(SCRIPT),
                    "-Image",
                    "fake-ech-workers",
                ],
                cwd=ROOT,
                env=env,
                capture_output=True,
                text=True,
                timeout=30,
            )

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertIn("[ech-workers-smoke] success", result.stdout)


if __name__ == "__main__":
    unittest.main()
