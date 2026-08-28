#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import sys
from typing import Any


def resource_ids(value: Any) -> set[str]:
    found: set[str] = set()
    if isinstance(value, dict):
        for key in ("uuid", "database_id", "id"):
            item = value.get(key)
            if isinstance(item, str) and item.strip():
                found.add(item.strip())
        for key in ("result", "database"):
            if key in value:
                found.update(resource_ids(value[key]))
    elif isinstance(value, list):
        for item in value:
            found.update(resource_ids(item))
    return found


def exact_named_resource_id(value: Any, name: str) -> str:
    if isinstance(value, dict):
        value = value.get("result")
    if not isinstance(value, list):
        raise ValueError("Cloudflare resource list is not an array")
    matches = [item for item in value if isinstance(item, dict) and str(item.get("name", "")).strip() == name]
    if len(matches) != 1:
        raise ValueError(f"expected exactly one Cloudflare resource named {name!r}, got {len(matches)}")
    found = resource_ids(matches[0])
    if len(found) != 1:
        raise ValueError(f"expected exactly one ID for Cloudflare resource {name!r}, got {len(found)}")
    return next(iter(found))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--name", default="")
    args = parser.parse_args()
    payload = json.load(sys.stdin)
    if args.name:
        print(exact_named_resource_id(payload, args.name))
        return 0
    found = sorted(resource_ids(payload))
    if len(found) != 1:
        raise SystemExit(f"expected exactly one Cloudflare resource ID, got {len(found)}")
    print(found[0])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
