"""应用拥有的 Linux/x64 固定工具；全新目录安装，不读取或继承部署凭据。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import platform
import ssl
import subprocess
import sys
import tarfile
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, HTTPSHandler, ProxyHandler, Request, build_opener

from sources import inspect_checkout


class ToolchainError(RuntimeError):
    pass


def require(value, code):
    if not value:
        raise ToolchainError("EASYPROXY_SBA_TOOLCHAIN_" + code)


# Official release metadata and npm tarball/binary digests verified 2026-10-26.
PACKAGES = {
    "node": {"url": "https://nodejs.org/dist/v22.23.3/node-v22.23.3-linux-x64.tar.xz",
             "sha256": "df450af89261115ef9f9e3830c3eeb2cc9213b63c720b1af623cb5dcbe2e02de",
             "prefix": "node-v22.23.3-linux-x64", "limit": 64 * 1024 * 1024},
    "npm": {"url": "https://registry.npmjs.org/npm/-/npm-10.9.7.tgz",
            "sha256": "15b45313dd6ca1c2c3da89e12f789d78b16fc174bd0e711cac399af6e457a670",
            "prefix": "package", "limit": 32 * 1024 * 1024},
    "esbuild": {"url": "https://registry.npmjs.org/@esbuild/linux-x64/-/linux-x64-0.28.2.tgz",
                "sha256": "9573bb2233aab0f9ea7647d5cca9726113cc1768de61d66b17267f4db84488f6",
                "prefix": "package", "limit": 16 * 1024 * 1024},
    "go": {"url": "https://go.dev/dl/go1.27.3.linux-amd64.tar.gz",
           "sha256": "65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40",
           "prefix": "go", "limit": 80 * 1024 * 1024},
}
HOSTS = {"nodejs.org", "registry.npmjs.org", "go.dev", "dl.google.com"}


@dataclass(frozen=True)
class LinuxTools:
    root: Path
    node: Path
    npm_cli: Path
    esbuild: Path
    go: Path


class Redirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        target = urlsplit(newurl)
        require(req.get_method() == "GET" and target.scheme == "https" and target.netloc in HOSTS
                and not target.username and not target.password, "SOURCE")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _fetch(package: dict) -> bytes:
    url = urlsplit(package["url"])
    require(url.scheme == "https" and url.netloc in HOSTS and not url.username and not url.password, "SOURCE")
    opener = build_opener(ProxyHandler({}), HTTPSHandler(context=ssl.create_default_context()), Redirect())
    try:
        with opener.open(Request(package["url"], headers={"User-Agent": "EasyProxy-SBA-toolchain"}), timeout=90) as response:
            body = response.read(package["limit"] + 1)
        require(len(body) <= package["limit"] and hashlib.sha256(body).hexdigest() == package["sha256"], "DIGEST")
        return body
    except OSError:
        raise ToolchainError("EASYPROXY_SBA_TOOLCHAIN_DOWNLOAD") from None


def _extract(body: bytes, prefix: str, target: Path):
    """不使用 archive 提供的路径或链接写出目标目录；只复制普通文件和目录。"""
    target.mkdir(mode=0o700)
    with tarfile.open(fileobj=io.BytesIO(body), mode="r:*") as source:
        count = total = 0
        names = set()
        for member in source:
            count += 1
            path = PurePosixPath(member.name)
            require(not path.is_absolute() and ".." not in path.parts and "\\" not in member.name
                    and path.parts and path.parts[0] == prefix and count <= 100000, "ARCHIVE")
            parts = path.parts[1:]
            if not parts:
                require(member.isdir(), "ARCHIVE")
                continue
            # Install only Node itself. npm has its own exact version/archive;
            # never accidentally use the version bundled by a Node release.
            if prefix.startswith("node-v") and parts != ("bin", "node"):
                continue
            require(member.isdir() or member.isfile(), "ARCHIVE")
            relative = str(PurePosixPath(*parts))
            require(relative not in names, "ARCHIVE")
            names.add(relative)
            destination = target.joinpath(*parts)
            require(target.resolve() in destination.resolve().parents, "ARCHIVE")
            if member.isdir():
                destination.mkdir(mode=0o700, parents=True, exist_ok=True)
                continue
            total += member.size
            require(0 <= member.size <= 128 * 1024 * 1024 and total <= 600 * 1024 * 1024, "ARCHIVE")
            destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with source.extractfile(member) as incoming, destination.open("xb") as output:
                # Read the declared, bounded file; reject truncated archives.
                data = incoming.read(member.size + 1)
                require(len(data) == member.size, "ARCHIVE")
                output.write(data)
            destination.chmod(0o700 if member.mode & 0o111 else 0o600)


def tool_environment(tools: LinuxTools, output: Path) -> dict:
    home = output / "home"
    home.mkdir(mode=0o700)
    return {"PATH": str(tools.node.parent) + os.pathsep + os.defpath, "HOME": str(home),
            "TMPDIR": str(output), "TEMP": str(output), "TMP": str(output), "CI": "true",
            "GOTOOLCHAIN": "local", "GOWORK": "off", "GOPROXY": "https://proxy.golang.org",
            "GOSUMDB": "sum.golang.org", "CGO_ENABLED": "0", "GOFLAGS": "-mod=readonly",
            "GOCACHE": str(output / "go-cache"), "GOPATH": str(output / "go-path"),
            "GOROOT": str(tools.go.parents[1])}


def _run(args: list[str], output: Path, env: dict, label: str, timeout: int = 120) -> bytes:
    try:
        result = subprocess.run(args, cwd=output, env=env, stdin=subprocess.DEVNULL,
                                capture_output=True, timeout=timeout, check=False)
        (output / (label + ".log")).write_bytes(result.stdout + result.stderr)
        require(result.returncode == 0, "EXECUTION")
        return result.stdout.strip()
    except (OSError, subprocess.SubprocessError):
        raise ToolchainError("EASYPROXY_SBA_TOOLCHAIN_EXECUTION") from None


def prepare_linux_tools(output: Path) -> LinuxTools:
    require(sys.platform == "linux" and platform.machine().lower() == "x86_64", "HOST")
    require(output.is_absolute() and output.resolve() == output and not output.exists(), "PATH")
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    try:
        for name, package in PACKAGES.items():
            _extract(_fetch(package), package["prefix"], output / name)
        tools = LinuxTools(output, output / "node/bin/node", output / "npm/bin/npm-cli.js",
                           output / "esbuild/bin/esbuild", output / "go/bin/go")
        env = tool_environment(tools, output)
        require(_run([str(tools.node), "--version"], output, env, "node-version") == b"v22.23.3", "VERSION")
        require(_run([str(tools.node), str(tools.npm_cli), "--version"], output, env, "npm-version") == b"10.9.7", "VERSION")
        require(_run([str(tools.esbuild), "--version"], output, env, "esbuild-version") == b"0.28.2", "VERSION")
        require(_run([str(tools.go), "version"], output, env, "go-version") == b"go version go1.27.3 linux/amd64", "VERSION")
        provenance = {"archives": {name: row["sha256"] for name, row in PACKAGES.items()},
                      "files": {name: hashlib.sha256(path.read_bytes()).hexdigest() for name, path in
                                (("node", tools.node), ("npm-cli", tools.npm_cli), ("esbuild", tools.esbuild), ("go", tools.go))}}
        (output / "provenance.json").write_text(json.dumps(provenance, indent=2), encoding="utf-8")
        return tools
    except (OSError, ValueError, tarfile.TarError):
        raise ToolchainError("EASYPROXY_SBA_TOOLCHAIN_INSTALL") from None


def build_cli(root: Path, repository: str, sha: str, tools: LinuxTools, output: Path) -> Path:
    require(root.is_absolute(), "PATH")
    root = root.resolve(strict=True)
    inspect_checkout(root, repository, sha)
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and root.resolve() not in output.parents, "PATH")
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    env = tool_environment(tools, output)
    binary = output / "easyproxyctl"
    _run([str(tools.go), "-C", str(root / "tools/easyproxyctl"), "build", "-trimpath", "-buildvcs=false",
          "-o", str(binary), "./cmd/easyproxyctl"], output, env, "cli-build", timeout=600)
    version = _run([str(binary), "version"], output, env, "cli-version")
    require(version == b"0.3.0", "VERSION")
    inspect_checkout(root, repository, sha)
    (output / "provenance.json").write_text(json.dumps({"sourceSha": sha, "goVersion": "1.27.3",
         "binarySha256": hashlib.sha256(binary.read_bytes()).hexdigest()}, indent=2), encoding="utf-8")
    return binary
