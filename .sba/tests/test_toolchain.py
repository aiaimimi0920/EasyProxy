"""固定工具的解包、环境和失败边界；真实官方安装另有 Linux 运行证据。"""
import hashlib
import io
from pathlib import Path
import tarfile

import pytest

from test_sources import sources  # Establish the same production import path.
import toolchain


def archive(name="package/bin/tool", body=b"tool", kind=tarfile.REGTYPE, link=""):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as stream:
        item = tarfile.TarInfo(name)
        item.type = kind
        item.linkname = link
        item.mode = 0o7777
        item.size = len(body) if kind == tarfile.REGTYPE else 0
        stream.addfile(item, io.BytesIO(body) if item.size else None)
    return output.getvalue()


def test_archive_only_writes_plain_files_below_new_directory(tmp_path):
    output = tmp_path / "tools"
    toolchain._extract(archive(), "package", output)
    assert (output / "bin/tool").read_bytes() == b"tool"
    if toolchain.sys.platform == "linux":
        assert (output / "bin/tool").stat().st_mode & 0o7777 == 0o700
    with pytest.raises(FileExistsError):
        toolchain._extract(archive(), "package", output)


def test_node_extraction_cannot_select_its_bundled_npm(tmp_path):
    output = tmp_path / "node"
    toolchain._extract(archive("node-v22.23.3-linux-x64/lib/node_modules/npm/bin/npm-cli.js"), "node-v22.23.3-linux-x64", output)
    assert not list(output.iterdir())


@pytest.mark.parametrize("name,kind,link", [
    ("/tmp/escape", tarfile.REGTYPE, ""),
    ("package/../../escape", tarfile.REGTYPE, ""),
    ("other/file", tarfile.REGTYPE, ""),
    ("package\\escape", tarfile.REGTYPE, ""),
    ("package/bin/tool", tarfile.SYMTYPE, "../../escape"),
    ("package/bin/tool", tarfile.LNKTYPE, "package/other"),
])
def test_archive_rejects_path_or_link_escape_before_file_write(tmp_path, name, kind, link):
    with pytest.raises(toolchain.ToolchainError, match="ARCHIVE"):
        toolchain._extract(archive(name, kind=kind, link=link), "package", tmp_path / "tools")
    assert not (tmp_path / "escape").exists()


def test_environment_does_not_inherit_auth_injection_or_alternate_go(monkeypatch, tmp_path):
    for name in ("CLOUDFLARE_API_TOKEN", "AWS_ACCESS_KEY_ID", "GITHUB_TOKEN", "NODE_OPTIONS", "GOFLAGS", "GOPRIVATE", "GOTOOLCHAIN", "PATH"):
        monkeypatch.setenv(name, "must-not-inherit")
    tools = toolchain.LinuxTools(tmp_path, tmp_path / "node/bin/node", tmp_path / "npm.js", tmp_path / "esbuild", tmp_path / "go/bin/go")
    env = toolchain.tool_environment(tools, tmp_path)
    assert "must-not-inherit" not in " ".join(env.values())
    assert env["GOTOOLCHAIN"] == "local" and env["GOSUMDB"] == "sum.golang.org"
    assert env["GOFLAGS"] == "-mod=readonly" and env["GOWORK"] == "off"
    assert env["PATH"].split(toolchain.os.pathsep)[0] == str(tools.node.parent)


@pytest.mark.parametrize("system,machine", [("win32", "AMD64"), ("linux", "aarch64")])
def test_install_rejects_unsupported_host_before_download(monkeypatch, tmp_path, system, machine):
    monkeypatch.setattr(toolchain.sys, "platform", system)
    monkeypatch.setattr(toolchain.platform, "machine", lambda: machine)
    monkeypatch.setattr(toolchain, "_fetch", lambda *_: pytest.fail("unexpected download"))
    with pytest.raises(toolchain.ToolchainError, match="HOST"):
        toolchain.prepare_linux_tools(tmp_path / "not-created")
    assert not (tmp_path / "not-created").exists()


@pytest.mark.parametrize("body,expected,limit", [(b"wrong", "0" * 64, 10), (b"too large", hashlib.sha256(b"too large").hexdigest(), 3)])
def test_download_rejects_digest_or_size_without_retry(monkeypatch, body, expected, limit):
    calls = []

    class Response(io.BytesIO):
        pass

    class Opener:
        def open(self, request, timeout):
            calls.append(request)
            return Response(body)

    monkeypatch.setattr(toolchain, "build_opener", lambda *_: Opener())
    with pytest.raises(toolchain.ToolchainError, match="DIGEST"):
        toolchain._fetch({"url": "https://nodejs.org/fixed", "sha256": expected, "limit": limit})
    assert len(calls) == 1


def test_redirect_cannot_leave_official_https_hosts():
    request = toolchain.Request("https://go.dev/fixed")
    with pytest.raises(toolchain.ToolchainError, match="SOURCE"):
        toolchain.Redirect().redirect_request(request, None, 302, "redirect", {}, "https://unapproved.invalid/file")


def test_cli_rejects_relative_source_before_inspection_or_execution(tmp_path, monkeypatch):
    monkeypatch.setattr(toolchain, "inspect_checkout", lambda *_: pytest.fail("unexpected source inspection"))
    with pytest.raises(toolchain.ToolchainError, match="PATH"):
        toolchain.build_cli(Path("."), "fixture/easyproxy", "a" * 40, None, tmp_path / "not-created")
    assert not (tmp_path / "not-created").exists()
