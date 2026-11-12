import os
import shutil
import subprocess
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]
INSTALLER = ROOT / "deploy/native/windows/install-service.ps1"
POWERSHELL = shutil.which("pwsh") or shutil.which("powershell")
pytestmark = pytest.mark.skipif(os.name != "nt" or not POWERSHELL, reason="Windows PowerShell required")


def run_installer(tmp_path, *, what_if=False, no_service=False):
    # Exercise the actual installer with OS service operations replaced, not elevated.
    source = INSTALLER.read_text(encoding="utf-8")
    assert source.count("\nAssert-Administrator\n") == 1
    installer = tmp_path / "installer.ps1"
    installer.write_text(source.replace("\nAssert-Administrator\n", "\n"), encoding="utf-8")
    runner = tmp_path / "run.ps1"
    runner.write_text(r'''
param([string]$Root, [switch]$Preview, [switch]$NoService)
$ErrorActionPreference = 'Stop'
function Get-Service { if (-not $NoService) { [pscustomobject]@{ Name = 'EasyProxy' } } }
function Stop-Service { throw 'UNEXPECTED_SERVICE_STOP' }
function sc.exe { throw 'UNEXPECTED_SC_OPERATION' }
try {
    & (Join-Path $Root 'installer.ps1') -Version 'v-test' -PackagePath 'unused.zip' `
        -InstallRoot (Join-Path $Root 'install') -DataRoot (Join-Path $Root 'data') `
        -Confirm:$false -WhatIf:$Preview
    exit 0
} catch {
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}
''', encoding="utf-8")
    command = [POWERSHELL, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
               "-File", str(runner), "-Root", str(tmp_path)]
    if what_if:
        command.append("-Preview")
    if no_service:
        command.append("-NoService")
    return subprocess.run(command, cwd=ROOT, capture_output=True, text=True)


def test_duplicate_version_does_not_delete_existing_release(tmp_path):
    release = tmp_path / "install/releases/v-test"
    release.mkdir(parents=True)
    sentinel = release / "keep.txt"
    sentinel.write_text("existing release", encoding="utf-8")
    result = run_installer(tmp_path)
    assert result.returncode != 0
    assert "Release already installed" in result.stderr
    assert sentinel.read_text(encoding="utf-8") == "existing release"


def test_whatif_stops_before_install_or_service_operations(tmp_path):
    result = run_installer(tmp_path, what_if=True)
    assert result.returncode == 0, result.stderr
    assert not (tmp_path / "install").exists()
    assert not (tmp_path / "data").exists()
    assert "UNEXPECTED" not in result.stderr


def test_bad_archive_cleans_only_new_release_without_touching_services(tmp_path):
    result = run_installer(tmp_path, no_service=True)
    assert result.returncode != 0
    assert "unused.zip" in result.stderr
    assert "UNEXPECTED" not in result.stderr
    assert not (tmp_path / "install/releases/v-test").exists()
