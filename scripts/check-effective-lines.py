#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import subprocess
from dataclasses import dataclass
from pathlib import Path, PurePosixPath


REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_BASELINE = REPO_ROOT / "effective-lines-baseline.json"
SOFT_LIMIT = 500
HARD_LIMIT = 700

INCLUDED_PREFIXES = (
    ".github/workflows/",
    "deploy/",
    "scripts/",
    "service/base/cmd/",
    "service/base/frontend/src/",
    "service/base/internal/",
    "tests/",
    "tools/",
    "workers/ech-workers-cloudflare/",
)
INCLUDED_SUFFIXES = {
    ".bash",
    ".css",
    ".go",
    ".html",
    ".js",
    ".jsx",
    ".mjs",
    ".py",
    ".ps1",
    ".scss",
    ".sh",
    ".sql",
    ".ts",
    ".tsx",
    ".vue",
    ".yaml",
    ".yml",
}
INCLUDED_NAMES = {"Dockerfile"}


@dataclass(frozen=True)
class CommentSyntax:
    line_markers: tuple[str, ...]
    block_markers: tuple[tuple[str, str], ...]


SLASH_COMMENTS = CommentSyntax(("//",), (("/*", "*/"),))
HASH_COMMENTS = CommentSyntax(("#",), ())
POWERSHELL_COMMENTS = CommentSyntax(("#",), (("<#", "#>"),))
SQL_COMMENTS = CommentSyntax(("--",), (("/*", "*/"),))
VUE_COMMENTS = CommentSyntax(("//",), (("/*", "*/"), ("<!--", "-->")))
HTML_COMMENTS = CommentSyntax((), (("<!--", "-->"),))


def comment_syntax(path: PurePosixPath) -> CommentSyntax:
    suffix = path.suffix.lower()
    if suffix in {".go", ".js", ".jsx", ".mjs", ".ts", ".tsx", ".css", ".scss"}:
        return SLASH_COMMENTS
    if suffix == ".vue":
        return VUE_COMMENTS
    if suffix == ".ps1":
        return POWERSHELL_COMMENTS
    if suffix == ".sql":
        return SQL_COMMENTS
    if suffix in {".html"}:
        return HTML_COMMENTS
    return HASH_COMMENTS


@dataclass
class LexState:
    block_end: str = ""
    string_end: str = ""


def _quoted_end(source: str, start: int, marker: str) -> int:
    index = start
    while index < len(source):
        if source.startswith(marker, index):
            return index
        if marker in {'"', "'"} and source[index] == "\\":
            index += 2
        else:
            index += 1
    return -1


def _line_has_code(source: str, syntax: CommentSyntax, state: LexState) -> bool:
    output: list[str] = []
    index = 0

    while index < len(source):
        if state.block_end:
            end_index = source.find(state.block_end, index)
            if end_index < 0:
                return bool("".join(output).strip())
            index = end_index + len(state.block_end)
            state.block_end = ""
            continue

        if state.string_end:
            output.append(source[index:])
            end_index = _quoted_end(source, index, state.string_end)
            if end_index < 0:
                return bool("".join(output).strip())
            index = end_index + len(state.string_end)
            state.string_end = ""
            continue

        if any(source.startswith(marker, index) for marker in syntax.line_markers):
            break

        matched_block = next(
            ((start, end) for start, end in syntax.block_markers if source.startswith(start, index)),
            None,
        )
        if matched_block:
            state.block_end = matched_block[1]
            index += len(matched_block[0])
            continue

        quote = next(
            (marker for marker in ('"""', "'''", "`", '"', "'") if source.startswith(marker, index)),
            "",
        )
        if quote:
            output.append(quote)
            end_index = _quoted_end(source, index + len(quote), quote)
            if end_index < 0:
                if quote in {'"""', "'''", "`"}:
                    state.string_end = quote
                output.append(source[index + len(quote) :])
                break
            output.append(source[index + len(quote) : end_index + len(quote)])
            index = end_index + len(quote)
            continue

        output.append(source[index])
        index += 1

    return bool("".join(output).strip())


def count_effective_lines(source: str, path: PurePosixPath) -> int:
    syntax = comment_syntax(path)
    state = LexState()
    count = 0

    for raw_line in source.splitlines():
        if _line_has_code(raw_line, syntax, state):
            count += 1

    return count


def tracked_source_paths(repo_root: Path) -> list[PurePosixPath]:
    result = subprocess.run(
        ["git", "ls-files", "-z"],
        cwd=repo_root,
        capture_output=True,
        check=True,
    )
    paths: list[PurePosixPath] = []
    for raw_path in result.stdout.decode("utf-8").split("\0"):
        if not raw_path:
            continue
        path = PurePosixPath(raw_path)
        if not raw_path.startswith(INCLUDED_PREFIXES):
            continue
        if path.suffix.lower() in INCLUDED_SUFFIXES or path.name in INCLUDED_NAMES:
            paths.append(path)
    return sorted(paths, key=str)


def load_baseline(path: Path) -> dict[str, dict[str, object]]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if payload.get("schema_version") != 1:
        raise ValueError("effective-line baseline schema_version must be 1")
    if payload.get("hard_limit") != HARD_LIMIT or payload.get("soft_limit") != SOFT_LIMIT:
        raise ValueError("effective-line baseline limits do not match the checker")
    entries = payload.get("legacy_over_limit")
    if not isinstance(entries, dict):
        raise ValueError("legacy_over_limit must be an object")
    for source_path, entry in entries.items():
        if not isinstance(entry, dict) or not isinstance(entry.get("max_effective_lines"), int):
            raise ValueError(f"invalid baseline entry for {source_path}")
        if not str(entry.get("reason", "")).strip():
            raise ValueError(f"baseline entry lacks a reason: {source_path}")
        if not str(entry.get("plan_wave", "")).strip():
            raise ValueError(f"baseline entry lacks a plan_wave: {source_path}")
    return entries


def inspect_repository(repo_root: Path, baseline_path: Path) -> int:
    baseline = load_baseline(baseline_path)
    counts: dict[str, int] = {}
    warnings: list[tuple[str, int]] = []
    failures: list[str] = []

    for relative_path in tracked_source_paths(repo_root):
        path = repo_root / Path(*relative_path.parts)
        count = count_effective_lines(path.read_text(encoding="utf-8-sig"), relative_path)
        source_path = relative_path.as_posix()
        counts[source_path] = count
        if count > HARD_LIMIT:
            entry = baseline.get(source_path)
            if entry is None:
                failures.append(f"new hard-limit violation: {source_path} ({count} > {HARD_LIMIT})")
            elif count > int(entry["max_effective_lines"]):
                failures.append(
                    f"legacy debt grew: {source_path} ({count} > {entry['max_effective_lines']})"
                )
        elif count > SOFT_LIMIT:
            warnings.append((source_path, count))

    for source_path in sorted(baseline):
        count = counts.get(source_path)
        if count is None:
            failures.append(f"stale baseline entry for missing/non-source file: {source_path}")
        elif count <= HARD_LIMIT:
            failures.append(
                f"stale baseline entry: {source_path} is now {count} lines; remove its exception"
            )

    for source_path, count in warnings:
        print(f"WARN soft-limit file: {source_path} ({count} > {SOFT_LIMIT})")
    if failures:
        print("Effective-line gate failed:")
        for failure in failures:
            print(f"- {failure}")
        return 1

    debt_count = sum(1 for count in counts.values() if count > HARD_LIMIT)
    print(
        f"Effective-line gate passed: {len(counts)} files, "
        f"{debt_count} ratcheted legacy violations, {len(warnings)} soft-limit warnings"
    )
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Enforce EasyProxy effective-line limits with a debt ratchet.")
    parser.add_argument("--repo-root", type=Path, default=REPO_ROOT)
    parser.add_argument("--baseline", type=Path, default=DEFAULT_BASELINE)
    args = parser.parse_args()
    return inspect_repository(args.repo_root.resolve(), args.baseline.resolve())


if __name__ == "__main__":
    raise SystemExit(main())
