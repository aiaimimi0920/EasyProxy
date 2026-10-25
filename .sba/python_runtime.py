"""由平台提供的 CPython 3.12 建立全新 venv；只安装准确锁定的官方 wheel。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys


LOCK_SHA256 = "bbf11bc6a350b708f830e231a2f8c7e9338db1cd890db204c71d76c351014891"


class PythonRuntimeError(RuntimeError):
    pass


def require(value, code):
    if not value:
        raise PythonRuntimeError("EASYPROXY_SBA_PYTHON_" + code)


@dataclass(frozen=True)
class PythonRuntime:
    root: Path
    python: Path


def environment(output: Path) -> dict:
    return {"PATH": os.defpath, "HOME": str(output / "home"), "TMPDIR": str(output),
            "TEMP": str(output), "TMP": str(output), "CI": "true", "PIP_CONFIG_FILE": os.devnull,
            "PYTHONDONTWRITEBYTECODE": "1", "PYTHONNOUSERSITE": "1", "PYTHONUTF8": "1"}


def _run(args: list[str], output: Path, env: dict, label: str, timeout: int) -> None:
    try:
        with (output / (label + ".log")).open("xb") as log:
            result = subprocess.run(args, cwd=output, env=env, stdin=subprocess.DEVNULL,
                                    stdout=log, stderr=subprocess.STDOUT, timeout=timeout, check=False)
        require(result.returncode == 0, "INSTALL")
    except (OSError, subprocess.SubprocessError):
        raise PythonRuntimeError("EASYPROXY_SBA_PYTHON_INSTALL") from None


def prepare_python(lock: Path, output: Path) -> PythonRuntime:
    require(sys.platform == "linux" and platform.machine().lower() == "x86_64"
            and sys.implementation.name == "cpython" and sys.version_info[:2] == (3, 12), "HOST")
    require(lock.is_absolute() and lock.is_file() and not lock.is_symlink()
            and lock.resolve(strict=True) == lock, "LOCK")
    require(hashlib.sha256(lock.read_bytes()).hexdigest() == LOCK_SHA256, "LOCK")
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and lock.parent not in output.parents, "PATH")
    output.mkdir(mode=0o700, parents=True)
    (output / "home").mkdir(mode=0o700)
    env = environment(output)
    _run([sys.executable, "-I", "-m", "venv", "--copies", str(output / "venv")], output, env, "venv", 90)
    python = output / "venv/bin/python"
    require(python.is_file() and not python.is_symlink(), "INSTALL")
    _run([str(python), "-I", "-m", "pip", "--isolated", "install", "--no-index", "--require-hashes",
          "--only-binary=:all:", "--ignore-installed", "--no-cache-dir", "--no-compile", "--no-input",
          "--disable-pip-version-check", "--report", str(output / "install-report.json"), "-r", str(lock)],
         output, env, "wheel-install", 600)
    _run([str(python), "-I", "-m", "pip", "--isolated", "check"], output, env, "dependency-check", 30)
    (output / "provenance.json").write_text(json.dumps({"lockSha256": LOCK_SHA256,
        "pythonVersion": list(sys.version_info[:3]), "requireHashes": True, "onlyBinary": True,
        "indexFallback": False}, indent=2), encoding="utf-8")
    return PythonRuntime(output, python)
