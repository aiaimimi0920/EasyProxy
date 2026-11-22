from __future__ import annotations

import json
import subprocess
import time
from typing import Any


CONTAINER_BATCH_PROXY_PROBE = """
import json
import ssl
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

cfg = json.loads(sys.stdin.read())
proxies = cfg["proxies"]
targets = cfg["targets"]
req_timeout = max(1, int(cfg["timeout"]))
accept_below_500 = bool(cfg["accept_below_500"])
retries = max(1, int(cfg["retries"]))
retry_delay = max(0, int(cfg["retry_delay"]))
budget = cfg.get("budget")
deadline = (time.monotonic() + float(budget)) if budget is not None else None
ssl_ctx = ssl.create_default_context()

def probe_one(proxy_url):
    last = {"ok": False, "attempts": [], "error": "probe not started"}
    for idx in range(retries):
        if deadline is not None and time.monotonic() >= deadline:
            return proxy_url, last
        attempts = []
        for item in targets:
            if deadline is not None and time.monotonic() >= deadline:
                last = {"ok": False, "attempts": attempts, "error": "audit probe deadline exceeded"}
                return proxy_url, last
            url = item["url"]
            expected = set(item["expected"])
            left = req_timeout if deadline is None else max(1, min(req_timeout, int(deadline - time.monotonic()) + 1))
            opener = urllib.request.build_opener(
                urllib.request.ProxyHandler({"http": proxy_url, "https": proxy_url}),
                urllib.request.HTTPSHandler(context=ssl_ctx),
            )
            code, exit_code, stderr = 0, 0, ""
            try:
                with opener.open(url, timeout=left) as resp:
                    code = int(resp.getcode() or 0)
            except urllib.error.HTTPError as exc:
                code = int(exc.code or 0)
            except Exception as exc:
                exit_code = 7
                stderr = type(exc).__name__
            ok = exit_code == 0 and (code in expected or (accept_below_500 and 200 <= code < 500 and code != 407))
            attempts.append({"target": url, "status_code": code, "exit_code": exit_code, "ok": ok, "stderr": stderr[:400]})
            if ok:
                return proxy_url, {"ok": True, "attempts": attempts, "winning_target": url, "winning_status": code}
        last = {"ok": False, "attempts": attempts}
        if idx < retries - 1 and retry_delay > 0:
            if deadline is not None and time.monotonic() + retry_delay >= deadline:
                break
            time.sleep(retry_delay)
    return proxy_url, last

workers = max(1, min(int(cfg.get("workers", 64)), len(proxies)))
with ThreadPoolExecutor(max_workers=workers) as pool:
    out = dict(pool.map(probe_one, proxies))
sys.stdout.write(json.dumps(out))
""".strip()


def batch_probe_in_container(
    proxy_urls: list[str],
    policy: dict[str, Any],
    *,
    network_container: str,
    max_workers: int,
    deadline: float | None,
) -> dict[str, dict[str, Any]] | None:
    targets = []
    max_targets = max(1, int(policy.get("direct_probe_max_targets", 2)))
    for item in (policy.get("http_probe_targets") or [])[:max_targets]:
        url = str(item.get("url") or "").strip()
        expected = [int(code) for code in (item.get("expected_status") or [])]
        if url and expected:
            targets.append({"url": url, "expected": expected})
    if not targets or not proxy_urls:
        return {}
    budget: float | None = None
    if deadline is not None:
        remaining = deadline - time.time()
        if remaining <= 0:
            return {url: {"ok": False, "attempts": [], "error": "audit probe deadline exceeded"} for url in proxy_urls}
        budget = max(1.0, remaining)
    req_timeout = min(
        max(1, int(policy.get("request_timeout_seconds", 25))),
        max(1, int(policy.get("direct_probe_timeout_seconds", 10))),
    )
    payload = json.dumps({
        "proxies": proxy_urls,
        "targets": targets,
        "timeout": req_timeout,
        "accept_below_500": bool(policy.get("accept_http_status_below_500", True)),
        "retries": max(1, int(policy.get("direct_probe_retries", 1))),
        "retry_delay": max(0, int(policy.get("direct_probe_retry_delay_seconds", 1))),
        "budget": budget,
        "workers": max(1, max_workers),
    })
    proc_timeout = int(budget) + 15 if budget is not None else max(60, len(proxy_urls) * 5)
    try:
        completed = subprocess.run(
            ["docker", "exec", "-i", network_container.strip(), "python3", "-c", CONTAINER_BATCH_PROXY_PROBE],
            input=payload,
            text=True,
            capture_output=True,
            timeout=proc_timeout,
            check=False,
        )
        if completed.returncode == 0 and completed.stdout.strip():
            parsed = json.loads(completed.stdout)
            if isinstance(parsed, dict):
                return parsed
    except Exception:
        return None
    return None
