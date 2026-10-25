"""固定批准源码构建的 stdin-only sealed-box；不将秘密放到 argv、环境或日志。"""
from __future__ import annotations

import base64
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import subprocess

from sources import inspect_checkout
from toolchain import LinuxTools, _run, tool_environment


class SealError(RuntimeError):
    pass


def require(value):
    if not value:
        raise SealError("EASYPROXY_SBA_SEAL_REJECTED")


def secret_value(value) -> bool:
    try:
        return (type(value) is str and 0 < len(value.encode("utf-8")) <= 512
                and not any(char in value for char in ("\r", "\n", "\x00"))
                and value != "verification-not-applicable")
    except UnicodeError:
        return False


def decode_base64(value: str, size: int) -> bytes:
    try:
        raw = base64.b64decode(value, validate=True)
        require(len(raw) == size and base64.b64encode(raw).decode("ascii") == value)
        return raw
    except (ValueError, UnicodeError):
        raise SealError("EASYPROXY_SBA_SEAL_REJECTED") from None


@dataclass(frozen=True)
class Sealer:
    executable: Path
    source_sha: str
    sha256: str

    def encrypt(self, public_key: str, plaintext: str) -> str:
        require(secret_value(plaintext) and type(public_key) is str)
        decode_base64(public_key, 32)
        require(self.executable.is_absolute() and not self.executable.is_symlink()
                and self.executable.is_file()
                and hashlib.sha256(self.executable.read_bytes()).hexdigest() == self.sha256)
        names = {"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "SYSTEMDRIVE"}
        env = {key: value for key, value in os.environ.items() if key.upper() in names}
        try:
            result = subprocess.run([str(self.executable)], input=json.dumps(
                {"publicKey": public_key, "plaintext": plaintext}, ensure_ascii=True).encode("utf-8"),
                env=env, cwd=self.executable.parent, capture_output=True, timeout=10, check=False)
            require(result.returncode == 0 and result.stderr == b"" and len(result.stdout) <= 800)
            encoded = result.stdout.decode("ascii").removesuffix("\n")
            decode_base64(encoded, len(plaintext.encode("utf-8")) + 48)
            return encoded
        except (OSError, UnicodeError, subprocess.SubprocessError):
            raise SealError("EASYPROXY_SBA_SEAL_REJECTED") from None


def build_sealer(root: Path, repository: str, source_sha: str, tools: LinuxTools, output: Path) -> Sealer:
    require(root.is_absolute() and root.resolve(strict=True) == root)
    inspect_checkout(root, repository, source_sha)
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and root not in output.parents and root != output)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    binary = output / "sba-seal"
    env = tool_environment(tools, output)
    _run([str(tools.go), "-C", str(root / "tools/easyproxyctl"), "build", "-trimpath", "-buildvcs=false",
          "-o", str(binary), "./cmd/sba-seal"], output, env, "seal-build", timeout=600)
    inspect_checkout(root, repository, source_sha)
    result = Sealer(binary, source_sha, hashlib.sha256(binary.read_bytes()).hexdigest())
    (output / "provenance.json").write_text(json.dumps(
        {"sourceSha": source_sha, "goVersion": "1.27.3", "binarySha256": result.sha256}, indent=2), encoding="utf-8")
    return result
