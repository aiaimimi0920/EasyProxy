#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description="Materialize a runtime MiSub wrangler config for GitHub-hosted deploys.")
    parser.add_argument("--base-config", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--project-name", required=True)
    parser.add_argument("--public-url", required=True)
    parser.add_argument("--callback-url", required=True)
    parser.add_argument("--d1-binding", default="MISUB_DB")
    parser.add_argument("--d1-database-name", required=True)
    parser.add_argument("--d1-database-id", required=True)
    args = parser.parse_args()

    base_path = Path(args.base_config).resolve()
    output_path = Path(args.output).resolve()

    config = json.loads(base_path.read_text(encoding="utf-8"))
    config["name"] = args.project_name
    config.setdefault("vars", {})
    config["vars"]["MISUB_PUBLIC_URL"] = args.public_url
    config["vars"]["MISUB_CALLBACK_URL"] = args.callback_url
    config["d1_databases"] = [
        {
            "binding": args.d1_binding,
            "database_name": args.d1_database_name,
            "database_id": args.d1_database_id,
        }
    ]

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(json.dumps(config, ensure_ascii=False, indent=2), encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
