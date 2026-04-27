import argparse
import base64
import json
import pathlib
import sys
from typing import Callable

import requests


def validate_non_empty_text(content: bytes) -> None:
    text = content.decode("utf-8", errors="replace").strip()
    if not text:
        raise RuntimeError("response body is empty")


def validate_clash_yaml(content: bytes) -> None:
    text = content.decode("utf-8", errors="replace")
    if not any(marker in text for marker in ("proxies:", "proxy-groups:", "proxy-providers:")):
        raise RuntimeError("response does not look like a Clash config")


def validate_json_document(content: bytes) -> None:
    payload = json.loads(content.decode("utf-8"))
    if payload in ({}, [], "", None):
        raise RuntimeError("JSON payload is empty")


def validate_v2ray_payload(content: bytes) -> None:
    text = content.decode("utf-8", errors="replace").strip()
    if not text:
        raise RuntimeError("response body is empty")
    try:
        base64.b64decode(text.encode("utf-8"), validate=False)
    except Exception as exc:  # pragma: no cover - defensive
        raise RuntimeError("response does not look like a V2Ray subscription payload") from exc


def fetch_and_validate(base_url: str, path: str, validator: Callable[[bytes], None]) -> None:
    url = f"{base_url.rstrip('/')}/{path.lstrip('/')}"
    response = requests.get(url, timeout=30)
    response.raise_for_status()
    validator(response.content)
    print(f"verified {url}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify published aggregator artifacts are readable and well-formed.")
    parser.add_argument("--base-url", required=True, help="Public base URL serving the aggregator artifacts.")
    parser.add_argument("--runtime-config", required=True, help="Materialized runtime config JSON.")
    args = parser.parse_args()

    runtime_path = pathlib.Path(args.runtime_config)
    runtime = json.loads(runtime_path.read_text(encoding="utf-8"))
    items = runtime.get("storage", {}).get("items", {})

    required = {
        "public-clash": validate_clash_yaml,
        "public-v2ray": validate_v2ray_payload,
        "public-singbox": validate_json_document,
        "public-mixed": validate_non_empty_text,
        "crawledsubs": validate_json_document,
    }

    for item_name, validator in required.items():
        if item_name not in items:
            raise RuntimeError(f"Missing storage item in runtime config: {item_name}")
        key = items[item_name].get("key", "").strip()
        if not key:
            raise RuntimeError(f"Storage item {item_name} does not define a key")
        fetch_and_validate(args.base_url, key, validator)

    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
