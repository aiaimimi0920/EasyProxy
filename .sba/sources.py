"""将批准提交的 gitlink 准备到隔离目录；不执行构建或云写入。"""
from __future__ import annotations

import configparser
import os
from pathlib import Path
import re
import subprocess


class SourceError(RuntimeError):
    """仅返回固定诊断，不转发 Git 原始输出或私密路径。"""


PATHS = ("upstreams/aggregator", "upstreams/ech-workers", "upstreams/misub")
SHA = re.compile(r"[a-f0-9]{40}\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\Z")
URL = re.compile(r"https://github\.com/([A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99})\.git\Z")


def _ensure(condition: bool) -> None:
    if not condition:
        raise SourceError("EASYPROXY_SBA_SOURCE_REJECTED")


def _origin_matches(url: str, repository: str) -> bool:
    # GitHub repository identities are case-insensitive. Keep HTTPS/no-credentials
    # restrictions while sharing Coordinator's lower-case canonical identity.
    match = URL.fullmatch(url if url.endswith(".git") else url + ".git")
    return match is not None and match.group(1).lower() == repository.lower()


def _environment() -> dict[str, str]:
    names = {"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "SYSTEMDRIVE", "TEMP", "TMP", "HOME", "USERPROFILE"}
    result = {key: value for key, value in os.environ.items() if key.upper() in names}
    result.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull, GIT_TERMINAL_PROMPT="0", GIT_LFS_SKIP_SMUDGE="1")
    return result


def _git(root: Path, *args: str, timeout: int = 30) -> str:
    try:
        result = subprocess.run(["git", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false", "-c", "core.hooksPath=" + os.devnull,
                                 "-C", str(root), *args], env=_environment(), capture_output=True,
                                timeout=timeout, check=True, encoding="utf-8", errors="strict")
        _ensure(len(result.stdout.encode("utf-8")) <= 1024 * 1024)
        return result.stdout.rstrip("\r\n")
    except (OSError, UnicodeError, subprocess.SubprocessError):
        raise SourceError("EASYPROXY_SBA_SOURCE_PREPARATION_FAILED") from None


def read_gitlinks(root: Path, source_sha: str) -> list[dict[str, str]]:
    """仅读取 Git 对象，既不解析工作树 .gitmodules，也不查询浮动分支。"""
    _ensure(isinstance(source_sha, str) and SHA.fullmatch(source_sha) is not None)
    raw = _git(root, "show", source_sha + ":.gitmodules")
    parser = configparser.ConfigParser(interpolation=None, strict=True)
    try:
        parser.read_string(raw)
        _ensure(not parser.defaults() and len(parser.sections()) == len(PATHS))
        urls = {}
        for section in parser.sections():
            _ensure(section.startswith('submodule "') and section.endswith('"'))
            _ensure(set(parser[section]) == {"path", "url"})
            path, url = parser[section]["path"], parser[section]["url"]
            _ensure(path in PATHS and path not in urls and URL.fullmatch(url) is not None)
            _ensure(section == 'submodule "' + path + '"')
            urls[path] = url
    except configparser.Error:
        raise SourceError("EASYPROXY_SBA_SOURCE_REJECTED") from None
    rows = []
    for path in PATHS:
        line = _git(root, "ls-tree", source_sha, "--", path)
        match = re.fullmatch(r"160000 commit ([a-f0-9]{40})\t" + re.escape(path), line)
        _ensure(match is not None)
        rows.append({"path": path, "repository": urls[path], "sourceSha": match.group(1)})
    return rows


def inspect_checkout(root: Path, repository: str, source_sha: str) -> list[dict[str, str]]:
    root = Path(root).resolve(strict=True)
    _ensure(REPOSITORY.fullmatch(repository) is not None and SHA.fullmatch(source_sha) is not None)
    metadata = root / ".git"
    _ensure(metadata.is_dir() and not metadata.is_symlink() and metadata.resolve() == metadata)
    _ensure(Path(_git(root, "rev-parse", "--show-toplevel")).resolve() == root)
    _ensure(_git(root, "rev-parse", "HEAD") == source_sha)
    _ensure(_origin_matches(_git(root, "remote", "get-url", "origin"), repository))
    _ensure(not _git(root, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none"))
    tracked = _git(root, "ls-files", "-v", "-z").split("\0")
    _ensure(all(not entry or entry.startswith("H ") for entry in tracked))
    return read_gitlinks(root, source_sha)


def materialize_root(destination: Path, repository: str, source_sha: str,
                     cache_root: Path | None = None) -> list[dict[str, str]]:
    """默认分支的调度入口也只准备实例已批准根 SHA，不执行它或跟随 main。"""
    _ensure(isinstance(repository, str) and REPOSITORY.fullmatch(repository) is not None
            and isinstance(source_sha, str) and SHA.fullmatch(source_sha) is not None)
    destination = Path(destination)
    _ensure(destination.is_absolute() and not str(destination).startswith(("\\\\", "//")))
    destination = destination.resolve()
    _ensure(not destination.exists())
    url = f"https://github.com/{repository}.git"
    source = url
    if cache_root is not None:
        cache_root = Path(cache_root).resolve(strict=True)
        _ensure(destination != cache_root and cache_root not in destination.parents)
        try:
            if _origin_matches(_git(cache_root, "remote", "get-url", "origin"), repository):
                _git(cache_root, "cat-file", "-e", source_sha + "^{commit}")
                source = str(cache_root)
        except SourceError:
            pass  # 只读缓存对象不是来源权威；不采用它的 HEAD 或未提交文件。
    destination.mkdir(parents=True, exist_ok=False)
    _git(destination, "init", "--quiet")
    _git(destination, "remote", "add", "origin", url)
    _git(destination, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--depth=1", source, source_sha, timeout=120)
    _git(destination, "-c", "submodule.recurse=false", "checkout", "--quiet", "--detach", source_sha)
    return inspect_checkout(destination, repository, source_sha)


def materialize_gitlinks(root: Path, destination: Path, repository: str, source_sha: str,
                         cache_root: Path | None = None) -> list[dict[str, str]]:
    """不复用输出目录；中断后保留现场，由操作方显式选择新的工作目录。"""
    root = Path(root).resolve(strict=True)
    rows = inspect_checkout(root, repository, source_sha)
    destination = Path(destination)
    _ensure(destination.is_absolute() and not str(destination).startswith(("\\\\", "//")))
    destination = destination.resolve()
    _ensure(destination != root and root not in destination.parents and not destination.exists())
    if cache_root is not None:
        cache_root = Path(cache_root).resolve(strict=True)
    destination.mkdir(parents=True, exist_ok=False)
    for row in rows:
        target = destination / row["path"]
        target.mkdir(parents=True, exist_ok=False)
        _materialize_gitlink(row, target, cache_root)
    return rows


def _materialize_gitlink(row: dict[str, str], target: Path, cache_root: Path | None) -> None:
    _ensure(target.is_dir() and not target.is_symlink() and not any(target.iterdir()))
    _git(target, "init", "--quiet")
    _git(target, "remote", "add", "origin", row["repository"])
    source = row["repository"]
    cached = cache_root / row["path"] if cache_root else None
    if cached and cached.is_dir():
        try:
            if _git(cached, "remote", "get-url", "origin") == source:
                _git(cached, "cat-file", "-e", row["sourceSha"] + "^{commit}")
                source = str(cached)
        except SourceError:
            pass  # 缓存只提供准确对象，不采用缓存工作树或 HEAD。
    _git(target, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--depth=1", source, row["sourceSha"], timeout=120)
    _git(target, "-c", "submodule.recurse=false", "checkout", "--quiet", "--detach", row["sourceSha"])
    _ensure(_git(target, "rev-parse", "HEAD") == row["sourceSha"])
    _ensure(not _git(target, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"))


def materialize_workspace(destination: Path, repository: str, source_sha: str,
                          cache_root: Path | None = None) -> list[dict[str, str]]:
    """形成现有根脚本可直接构建的统一目录；不复制/移动缓存工作树。"""
    destination = Path(destination)
    rows = materialize_root(destination, repository, source_sha, cache_root=cache_root)
    destination = destination.resolve(strict=True)
    if cache_root is not None:
        cache_root = Path(cache_root).resolve(strict=True)
    for row in rows:
        target = destination / row["path"]
        target.mkdir(parents=True, exist_ok=True)  # Git checkout 可能已创建空 gitlink 目录。
        _materialize_gitlink(row, target, cache_root)
    _ensure(inspect_checkout(destination, repository, source_sha) == rows)
    return rows


def inspect_gitlink_checkout(root: Path, row: dict[str, str]) -> None:
    """构建前后检查已准备 gitlink，连 ignored 文件和 skip-worktree 也不能绕过。"""
    _ensure(type(row) is dict and set(row) == {"path", "repository", "sourceSha"}
            and row["path"] in PATHS and URL.fullmatch(row["repository"]) is not None
            and SHA.fullmatch(row["sourceSha"]) is not None)
    target = root / row["path"]
    _ensure(target.is_dir() and not target.is_symlink() and target.resolve(strict=True) == target)
    metadata = target / ".git"
    _ensure(metadata.is_dir() and not metadata.is_symlink() and metadata.resolve() == metadata)
    _ensure(Path(_git(target, "rev-parse", "--show-toplevel")).resolve() == target)
    _ensure(_git(target, "rev-parse", "HEAD") == row["sourceSha"]
            and _git(target, "remote", "get-url", "origin") == row["repository"])
    _ensure(not _git(target, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"))
    _ensure(all(not entry or entry.startswith("H ") for entry in _git(target, "ls-files", "-v", "-z").split("\0")))
