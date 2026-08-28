#!/usr/bin/env python3

from __future__ import annotations

import argparse
from pathlib import Path

import yaml


def materialize(base: Path, output: Path, pages_project: str, d1_database: str) -> None:
    payload = yaml.safe_load(base.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError("base topology must be a YAML object")
    resources = payload.setdefault("cloudflare", {}).setdefault("resources", {})
    resources["pages_project"] = pages_project.strip()
    resources["d1_database"] = d1_database.strip()
    if not resources["pages_project"] or not resources["d1_database"]:
        raise ValueError("Pages project and D1 database names must not be empty")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(yaml.safe_dump(payload, sort_keys=False, allow_unicode=True), encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description="Materialize non-secret Cloudflare resource names in an EasyProxy topology.")
    parser.add_argument("--base", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--pages-project", required=True)
    parser.add_argument("--d1-database", required=True)
    args = parser.parse_args()
    materialize(Path(args.base), Path(args.output), args.pages_project, args.d1_database)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
