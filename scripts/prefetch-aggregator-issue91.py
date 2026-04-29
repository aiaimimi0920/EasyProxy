#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import urllib.request

import yaml


USER_AGENT = "Mozilla/5.0; Clash.Meta; Mihomo; Shadowrocket;"
ISSUE91_DOMAIN_NAME = "seed-sub-issue91-shared"
FILEPATH_PROTOCOL = "file:///"


def fetch_text_urllib(url: str, timeout: int) -> str:
    request = urllib.request.Request(url=url, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        if response.getcode() != 200:
            raise RuntimeError(f"issue91 prefetch failed with status={response.getcode()}")
        return response.read().decode("utf-8", errors="replace").strip()


def fetch_text_curl(url: str, timeout: int) -> str:
    completed = subprocess.run(
        [
            "curl",
            "-L",
            "--max-time",
            str(timeout),
            "-A",
            USER_AGENT,
            url,
        ],
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    if completed.returncode != 0:
        raise RuntimeError(f"curl prefetch failed with exit code={completed.returncode}: {completed.stderr.strip()}")
    return completed.stdout.strip()


def fetch_text(url: str, timeout: int) -> str:
    errors: list[str] = []
    for label, fetcher in (("urllib", fetch_text_urllib), ("curl", fetch_text_curl)):
        try:
            return fetcher(url, timeout)
        except Exception as exc:
            errors.append(f"{label}: {exc}")
    raise RuntimeError("issue91 prefetch failed via all strategies: " + " | ".join(errors))


def ensure_proxy_count(text: str) -> int:
    payload = yaml.safe_load(text)
    if not isinstance(payload, dict):
        raise RuntimeError("issue91 response is not a mapping document")
    proxies = payload.get("proxies")
    if not isinstance(proxies, list):
        raise RuntimeError("issue91 response does not contain a valid proxies list")
    count = len(proxies)
    if count <= 0:
        raise RuntimeError("issue91 response contained zero proxies")
    return count


def main() -> int:
    parser = argparse.ArgumentParser(description="Prefetch the live Issue #91 subscription into a local file seed.")
    parser.add_argument("--runtime-config", required=True, help="Path to the materialized aggregator runtime config JSON.")
    parser.add_argument("--output", required=True, help="Path to store the prefetched Issue #91 Clash YAML.")
    parser.add_argument("--timeout", type=int, default=120, help="HTTP request timeout in seconds.")
    args = parser.parse_args()

    runtime_path = pathlib.Path(args.runtime_config)
    output_path = pathlib.Path(args.output)

    runtime = json.loads(runtime_path.read_text(encoding="utf-8"))
    domains = runtime.get("domains") or []
    target = None
    for entry in domains:
        if isinstance(entry, dict) and str(entry.get("name") or "").strip() == ISSUE91_DOMAIN_NAME:
            target = entry
            break

    if not target:
        raise RuntimeError(f"missing {ISSUE91_DOMAIN_NAME} in runtime config")

    subs = target.get("sub") or []
    if not isinstance(subs, list) or not subs or not isinstance(subs[0], str) or not subs[0].strip():
        raise RuntimeError(f"{ISSUE91_DOMAIN_NAME} does not contain a usable subscription URL")

    subscription_url = subs[0].strip()
    text = fetch_text(subscription_url, args.timeout)
    proxy_count = ensure_proxy_count(text)

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(text, encoding="utf-8")

    target["sub"] = [f"{FILEPATH_PROTOCOL}{output_path.resolve().as_posix()}"]
    runtime_path.write_text(json.dumps(runtime, ensure_ascii=False, indent=4), encoding="utf-8")

    print(
        json.dumps(
            {
                "domain": ISSUE91_DOMAIN_NAME,
                "prefetched_file": str(output_path.resolve()),
                "proxy_count": proxy_count,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
