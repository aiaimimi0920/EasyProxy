#!/usr/bin/env python3

from __future__ import annotations

import argparse
import subprocess
from pathlib import Path


IGNORED_PARTS = {".git", ".tmp", "node_modules"}


def collect_go_files(paths: list[str]) -> list[Path]:
    files: set[Path] = set()
    for raw_path in paths:
        path = Path(raw_path).resolve()
        if path.is_file():
            if path.suffix == ".go":
                files.add(path)
            continue
        if not path.is_dir():
            raise SystemExit(f"Go format path not found: {path}")
        for candidate in path.rglob("*.go"):
            relative_parts = candidate.relative_to(path).parts
            if not any(part in IGNORED_PARTS for part in relative_parts):
                files.add(candidate.resolve())
    return sorted(files)


def normalized_source(path: Path) -> str:
    return path.read_text(encoding="utf-8-sig").replace("\r\n", "\n").replace("\r", "\n")


def main() -> int:
    parser = argparse.ArgumentParser(description="Check Go formatting without treating CRLF as a source change.")
    parser.add_argument("paths", nargs="+", help="Go source files or directories to inspect")
    args = parser.parse_args()

    files = collect_go_files(args.paths)
    if not files:
        raise SystemExit("No Go files found in the requested paths.")

    unformatted: list[Path] = []
    for path in files:
        source = normalized_source(path)
        result = subprocess.run(
            ["gofmt"],
            input=source,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=120,
        )
        if result.returncode != 0:
            print(f"gofmt failed for {path}: {result.stderr.strip()}")
            return result.returncode
        if result.stdout != source:
            unformatted.append(path)

    if unformatted:
        print("Go files requiring gofmt:")
        for path in unformatted:
            print(path)
        return 1

    print(f"{len(files)} Go files are gofmt-clean")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
