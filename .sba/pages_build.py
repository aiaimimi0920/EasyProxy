"""从批准源码复制构建 MiSub；固定锁文件，Wrangler 只运行本地 functions build。"""
from __future__ import annotations

from dataclasses import dataclass
from email.parser import BytesParser
from email.policy import default
import hashlib
import json
import mimetypes
import os
from pathlib import Path
import re
import shutil
import subprocess

from pages_api import require, PagesError
from sources import inspect_checkout, inspect_gitlink_checkout


COMPATIBILITY_DATE = "2026-03-25"
VERSIONS = {"wrangler": "4.93.1", "vite": "7.3.3", "blake3-wasm": "2.1.5"}
NPM_VERSION = "10.9.7"
ADAPTERS = ("pages-middleware.mjs", "pages-assets.mjs", "misub-managed.mjs", "misub-body.mjs")


@dataclass(frozen=True)
class Asset:
    path: str
    body: bytes
    hash: str
    content_type: str


@dataclass(frozen=True)
class PagesArtifact:
    assets: tuple[Asset, ...]
    worker_bundle: bytes
    routes: bytes


def _plain_file(path: Path) -> bytes:
    require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "SOURCE")
    return path.read_bytes()


def _copy_source(source: Path, destination: Path):
    destination.mkdir(exist_ok=False)
    for child in source.iterdir():
        if child.name == ".git":
            continue
        require(not child.is_symlink() and child.resolve(strict=True) == child, "SOURCE")
        target = destination / child.name
        if child.is_dir():
            _copy_source(child, target)
        else:
            require(child.is_file(), "SOURCE")
            shutil.copyfile(child, target)


def _environment(output: Path, node: Path) -> dict:
    names = {"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "SYSTEMDRIVE"}
    env = {key: value for key, value in os.environ.items() if key.upper() in names}
    home = output / "home"
    home.mkdir()
    for name in ("npm-user.rc", "npm-global.rc"):
        (home / name).write_text("", encoding="utf-8")
    env.update(HOME=str(home), USERPROFILE=str(home), APPDATA=str(home), LOCALAPPDATA=str(home),
               TEMP=str(output), TMP=str(output), TMPDIR=str(output), CI="true", WRANGLER_SEND_METRICS="false",
               WRANGLER_LOG_PATH=str(output / "wrangler.log"), CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV="false",
               NPM_CONFIG_USERCONFIG=str(home / "npm-user.rc"), NPM_CONFIG_GLOBALCONFIG=str(home / "npm-global.rc"),
               NPM_CONFIG_REGISTRY="https://registry.npmjs.org", NPM_CONFIG_FETCH_RETRIES="0",
               NPM_CONFIG_CACHE=str(output / "npm-cache"))
    env["PATH"] = str(node.parent) + os.pathsep + env.get("PATH", os.defpath)
    return env


def _run(args: list[str], cwd: Path, env: dict, label: str, timeout: int = 120):
    try:
        result = subprocess.run(args, cwd=cwd, env=env, stdin=subprocess.DEVNULL, capture_output=True,
                                timeout=timeout, check=False)
        # Build logs never enter receipts or exceptions; the isolated environment
        # contains no provider token, R2 key, business secret or GitHub credential.
        (Path(env["TEMP"]) / (label + ".log")).write_bytes(result.stdout + result.stderr)
        require(result.returncode == 0, "BUILD_" + label.upper())
        return result.stdout.decode("utf-8", errors="strict").strip()
    except (OSError, UnicodeError, subprocess.SubprocessError):
        raise PagesError("EASYPROXY_SBA_PAGES_BUILD_" + label.upper()) from None


def _lock(raw: bytes):
    value = json.loads(raw)
    require(type(value) is dict and value.get("lockfileVersion") == 3 and type(value.get("packages")) is dict, "LOCK")
    for key, row in value["packages"].items():
        if not key:
            continue
        require(key.startswith("node_modules/") and type(row) is dict and row.get("link") is not True
                and isinstance(row.get("resolved"), str)
                and re.fullmatch(r"https://registry\.(?:npmjs\.org|npmmirror\.com)/[A-Za-z0-9@/_.-]+\.tgz", row["resolved"]) is not None
                and isinstance(row.get("integrity"), str) and row["integrity"].startswith("sha512-"), "LOCK")
        # The approved upstream lock contains historical mirror URLs. Fetch the
        # same path from npm's official registry with the SAME integrity, never
        # resolve versions again or modify the independent upstream lock.
        row["resolved"] = row["resolved"].replace("https://registry.npmmirror.com/", "https://registry.npmjs.org/", 1)
    for package, version in VERSIONS.items():
        require(value["packages"].get("node_modules/" + package, {}).get("version") == version, "TOOLCHAIN")
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


def _bundle(raw: bytes):
    require(0 < len(raw) <= 25 * 1024 * 1024 and raw.startswith(b"--"), "BUNDLE")
    boundary = raw.split(b"\r\n", 1)[0][2:]
    require(re.fullmatch(rb"[A-Za-z0-9_-]{1,128}", boundary) is not None, "BUNDLE")
    message = BytesParser(policy=default).parsebytes(b"Content-Type: multipart/form-data; boundary=" + boundary + b"\r\n\r\n" + raw)
    require(message.is_multipart() and not message.defects, "BUNDLE")
    parts = list(message.iter_parts())
    names = [part.get_param("name", header="content-disposition") for part in parts]
    require(len(parts) == 2 and len(set(names)) == 2 and "metadata" in names, "BUNDLE")
    metadata = json.loads(parts[names.index("metadata")].get_payload(decode=True))
    # The separately configured project owns bindings. Explicit empty bindings
    # in this nested metadata would override rather than inherit that authority.
    require(type(metadata) is dict and "bindings" not in metadata and isinstance(metadata.get("main_module"), str)
            and metadata["main_module"] in names, "BUNDLE")
    main = parts[names.index(metadata["main_module"])]
    require(main.get_content_type() == "application/javascript+module" and not main.defects, "BUNDLE")


def _assets(source: Path, manifest: Path) -> tuple[Asset, ...]:
    rows = json.loads(_plain_file(manifest))
    require(type(rows) is list and 0 < len(rows) <= 20000, "ASSETS")
    assets = []
    total = 0
    types = mimetypes.MimeTypes()
    for row in rows:
        require(type(row) is dict and set(row) == {"path", "hash", "sha256"}
                and isinstance(row["path"], str) and re.fullmatch(r"/(?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_.-]+", row["path"]) is not None
                and ".." not in row["path"] and re.fullmatch(r"[a-f0-9]{32}", row["hash"]) is not None, "ASSETS")
        body = _plain_file(source / row["path"][1:])
        require(len(body) <= 25 * 1024 * 1024 and hashlib.sha256(body).hexdigest() == row["sha256"], "ASSETS")
        total += len(body)
        require(total <= 256 * 1024 * 1024, "ASSETS")
        kind = "application/javascript" if row["path"].endswith((".js", ".mjs")) else types.guess_type(row["path"])[0]
        assets.append(Asset(row["path"], body, row["hash"], kind or "application/octet-stream"))
    require(len({item.path for item in assets}) == len(assets) and any(item.path == "/index.html" for item in assets), "ASSETS")
    return tuple(assets)


def build_pages(root: Path, repository: str, sha: str, node: Path, npm_cli: Path, workdir: Path) -> PagesArtifact:
    """所有安装/构建输出只进入全新目录；源码、独立 upstream 和开发依赖均不改。"""
    for path in (root, node, npm_cli, workdir):
        require(isinstance(path, Path) and path.is_absolute() and not str(path).startswith(("\\\\", "//")), "PATH")
    root = root.resolve(strict=True)
    output = workdir.resolve()
    require(root not in output.parents and output != root and not output.exists(), "PATH")
    rows = inspect_checkout(root, repository, sha)
    row = next(item for item in rows if item["path"] == "upstreams/misub")
    inspect_gitlink_checkout(root, row)
    try:
        source = root / row["path"]
        adapters = {name: _plain_file(root / ".sba" / name) for name in ADAPTERS}
        lock = _plain_file(source / "package-lock.json")
        canonical_lock = _lock(lock)
        require(node.is_file() and npm_cli.is_file(), "TOOLCHAIN")
        output.mkdir(parents=True, exist_ok=False)
        env = _environment(output, node)
        require(re.fullmatch(r"v22\.\d+\.\d+", _run([str(node), "--version"], output, env, "node")) is not None, "TOOLCHAIN")
        require(_run([str(node), str(npm_cli), "--version"], output, env, "npm") == NPM_VERSION, "TOOLCHAIN")
        project = output / "misub"
        _copy_source(source, project)
        (project / "package-lock.json").write_bytes(canonical_lock)
        # Secrets and instance-specific Wrangler config are never written here.
        # No config: functions build emits metadata without a bindings override.
        for config in ("wrangler.jsonc", "wrangler.json", "wrangler.toml"):
            path = project / config
            if path.exists():
                path.rename(project / (config + ".source"))
        require(not (project / "functions/_middleware.js").exists(), "SOURCE")
        (project / "functions/_middleware.js").write_bytes(adapters["pages-middleware.mjs"])
        require(not (project / "functions/sba-managed.js").exists(), "SOURCE")
        (project / "functions/sba-managed.js").write_bytes(adapters["misub-managed.mjs"])
        require(not (project / "functions/sba-body.mjs").exists(), "SOURCE")
        (project / "functions/sba-body.mjs").write_bytes(adapters["misub-body.mjs"])
        helper = output / "pages-assets.mjs"
        helper.write_bytes(adapters["pages-assets.mjs"])
        _run([str(node), str(npm_cli), "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--include=dev", "--include=optional", "--strict-ssl=true"],
             project, env, "install", timeout=600)
        require(_plain_file(project / "package-lock.json") == canonical_lock, "LOCK")
        for package, version in VERSIONS.items():
            require(json.loads(_plain_file(project / "node_modules" / package / "package.json")).get("version") == version, "TOOLCHAIN")
        _run([str(node), str(project / "node_modules/vite/bin/vite.js"), "build"], project, env, "frontend", timeout=300)
        _run([str(node), str(project / "node_modules/wrangler/bin/wrangler.js"), "pages", "functions", "build", "functions",
              "--outfile=" + str(output / "_worker.bundle"), "--build-output-directory=" + str(project / "dist"),
              "--output-routes-path=" + str(output / "_routes.json"), "--compatibility-date=" + COMPATIBILITY_DATE,
              "--metafile=" + str(output / "functions-meta.json")], project, env, "functions", timeout=180)
        _run([str(node), str(helper), str(project / "dist"), str(output / "assets.json")], project, env, "assets")
        bundle = _plain_file(output / "_worker.bundle")
        _bundle(bundle)
        routes = _plain_file(output / "_routes.json")
        routing = json.loads(routes)
        require(routing.get("version") == 1 and routing.get("include") == ["/*"] and routing.get("exclude") == [], "ROUTES")
        artifact = PagesArtifact(_assets(project / "dist", output / "assets.json"), bundle, routes)
    except (OSError, ValueError, UnicodeError, StopIteration):
        raise PagesError("EASYPROXY_SBA_PAGES_BUILD") from None
    inspect_gitlink_checkout(root, row)
    inspect_checkout(root, repository, sha)
    return artifact
