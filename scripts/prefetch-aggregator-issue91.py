#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import json
import os
import pathlib
import subprocess
import urllib.request

import requests
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


def fetch_text_requests(url: str, timeout: int) -> str:
    response = requests.get(
        url,
        headers={"User-Agent": USER_AGENT},
        timeout=timeout,
    )
    response.raise_for_status()
    return response.text.strip()


def fetch_text_curl(url: str, timeout: int) -> str:
    completed = subprocess.run(
        [
            "curl",
            "-L",
            "--max-time",
            str(timeout),
            "--compressed",
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


def fetch_text_curl_cffi(url: str, timeout: int) -> str:
    try:
        from curl_cffi import requests as curl_requests
    except Exception as exc:  # pragma: no cover - optional dependency
        raise RuntimeError(f"curl_cffi is unavailable: {exc}") from exc

    response = curl_requests.get(
        url,
        headers={
            "User-Agent": USER_AGENT,
            "Accept": "text/yaml, application/yaml, text/plain, */*",
            "Cache-Control": "no-cache",
            "Pragma": "no-cache",
        },
        timeout=timeout,
        impersonate="chrome",
    )
    response.raise_for_status()
    return response.text.strip()


def fetch_text(url: str, timeout: int) -> str:
    errors: list[str] = []

    def summarize(text: str) -> str:
        head = (text or "").replace("\r", " ").replace("\n", "\\n")
        return head[:200]

    for label, fetcher in (
        ("urllib", fetch_text_urllib),
        ("requests", fetch_text_requests),
        ("curl", fetch_text_curl),
        ("curl_cffi", fetch_text_curl_cffi),
    ):
        text = ""
        try:
            text = fetcher(url, timeout)
            proxy_count = ensure_proxy_count(text)
            print(
                json.dumps(
                    {
                        "strategy": label,
                        "validated_proxy_count": proxy_count,
                    },
                    ensure_ascii=False,
                )
            )
            return text
        except Exception as exc:
            preview = ""
            if "text" in locals():
                preview = summarize(text)
            errors.append(f"{label}: {exc} preview={preview}")
    raise RuntimeError("issue91 prefetch failed via all strategies: " + " | ".join(errors))


def decode_optional_base64_env(env_name: str) -> str:
    value = os.environ.get(env_name, "").strip()
    if not value:
        return ""
    try:
        return base64.b64decode(value).decode("utf-8").strip()
    except Exception as exc:
        raise RuntimeError(f"failed to decode {env_name}: {exc}") from exc


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
    parser.add_argument(
        "--fallback-url-env",
        default="EASYPROXY_AGGREGATOR_ISSUE91_UPSTREAM_URL_B64",
        help="Base64-encoded environment variable containing a fallback issue91 URL.",
    )
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
    fallback_url = decode_optional_base64_env(args.fallback_url_env)

    fetch_errors: list[str] = []
    text = ""
    source_url = subscription_url

    for candidate_url in [subscription_url, fallback_url]:
        candidate_url = str(candidate_url or "").strip()
        if not candidate_url:
            continue
        if text and candidate_url == source_url:
            continue
        try:
            text = fetch_text(candidate_url, args.timeout)
            source_url = candidate_url
            break
        except Exception as exc:
            fetch_errors.append(f"{candidate_url}: {exc}")

    if not text:
        raise RuntimeError("issue91 prefetch failed: " + " | ".join(fetch_errors))

    proxy_count = ensure_proxy_count(text)

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(text, encoding="utf-8")

    target["sub"] = [f"{FILEPATH_PROTOCOL}{output_path.resolve().as_posix()}"]
    runtime_path.write_text(json.dumps(runtime, ensure_ascii=False, indent=4), encoding="utf-8")

    print(
        json.dumps(
            {
                "domain": ISSUE91_DOMAIN_NAME,
                "source_url": source_url,
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
