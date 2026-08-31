from __future__ import annotations

import json
import os
import shutil
import subprocess
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any

import requests

try:
    from .easyproxy_source_audit_support import run
except ImportError:
    from easyproxy_source_audit_support import run


CONTAINER_PROXY_PROBE = """
import ssl
import sys
import urllib.error
import urllib.request

proxy_url, target_url, timeout = sys.argv[1:4]
context = ssl._create_unverified_context()
opener = urllib.request.build_opener(
    urllib.request.ProxyHandler({"http": proxy_url, "https": proxy_url}),
    urllib.request.HTTPSHandler(context=context),
)
try:
    with opener.open(target_url, timeout=int(timeout)) as response:
        print(response.getcode())
except urllib.error.HTTPError as exc:
    print(exc.code)
except Exception as exc:
    print(type(exc).__name__, file=sys.stderr)
    raise SystemExit(7)
""".strip()


def management_headers(management_password: str, *, json_content: bool = False) -> dict[str, str]:
    headers = {"Authorization": f"Bearer {management_password}"}
    if json_content:
        headers["Content-Type"] = "application/json"
    return headers


def stopped_container_exit_code(container_name: str) -> int | None:
    result = subprocess.run(
        ["docker", "inspect", container_name, "--format", "{{.State.Running}} {{.State.ExitCode}}"],
        capture_output=True,
        text=True,
        timeout=10,
        check=False,
    )
    if result.returncode != 0:
        return None
    fields = result.stdout.strip().split()
    if len(fields) != 2 or fields[0].lower() == "true":
        return None
    try:
        return int(fields[1])
    except ValueError:
        return None


def wait_management_ready(
    base_url: str,
    timeout_seconds: int,
    management_password: str,
    *,
    container_name: str = "",
) -> dict[str, Any]:
    deadline = time.time() + timeout_seconds
    last_error: Exception | None = None
    while time.time() < deadline:
        try:
            response = requests.get(
                f"{base_url}/api/settings",
                headers=management_headers(management_password),
                timeout=10,
            )
            response.raise_for_status()
            return response.json()
        except Exception as exc:
            last_error = exc
            exit_code = stopped_container_exit_code(container_name) if container_name else None
            if exit_code is not None:
                raise RuntimeError(
                    f"container {container_name} exited with code {exit_code} before management API became ready"
                ) from exc
            time.sleep(3)
    detail = f"; last error: {type(last_error).__name__}: {last_error}" if last_error else ""
    raise RuntimeError(f"timed out waiting for management API at {base_url}{detail}")

def wait_scenario_state(base_url: str, timeout_seconds: int, require_manifest_healthy: bool, require_fallback_active: bool, require_connector_instances: int, management_password: str) -> tuple[dict[str, Any], dict[str, Any] | None]:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        try:
            nodes_response = requests.get(
                f"{base_url}/api/nodes",
                headers=management_headers(management_password),
                timeout=15,
            )
            nodes_response.raise_for_status()
            nodes = nodes_response.json()
            source_sync = None
            try:
                source_sync_response = requests.get(
                    f"{base_url}/api/source-sync/status",
                    headers=management_headers(management_password),
                    timeout=10,
                )
                source_sync_response.raise_for_status()
                source_sync = source_sync_response.json()
            except Exception:
                source_sync = None

            total_nodes = int(nodes.get("total_nodes") or 0)
            connector_instances = int((source_sync or {}).get("connector_instance_count") or 0)

            if total_nodes <= 0:
                time.sleep(5)
                continue
            if require_manifest_healthy and not bool((source_sync or {}).get("manifest_healthy")):
                time.sleep(5)
                continue
            if require_fallback_active and not bool((source_sync or {}).get("fallback_active")):
                time.sleep(5)
                continue
            if require_connector_instances > 0 and connector_instances < require_connector_instances:
                time.sleep(5)
                continue
            return nodes, source_sync
        except Exception:
            time.sleep(5)
    raise RuntimeError(f"timed out waiting for scenario readiness at {base_url}")

def fetch_nodes_and_source_sync(base_url: str, management_password: str) -> tuple[dict[str, Any], dict[str, Any] | None]:
    nodes_response = requests.get(
        f"{base_url}/api/nodes",
        headers=management_headers(management_password),
        timeout=15,
    )
    nodes_response.raise_for_status()
    nodes = nodes_response.json()
    source_sync = None
    try:
        source_sync_response = requests.get(
            f"{base_url}/api/source-sync/status",
            headers=management_headers(management_password),
            timeout=10,
        )
        source_sync_response.raise_for_status()
        source_sync = source_sync_response.json()
    except Exception:
        source_sync = None
    return nodes, source_sync

def probe_http_proxy(proxy_url: str, policy: dict[str, Any], *, network_container: str = "") -> dict[str, Any]:
    attempts: list[dict[str, Any]] = []
    timeout = str(int(policy.get("request_timeout_seconds", 25)))
    accept_status_below_500 = bool(policy.get("accept_http_status_below_500", True))
    for item in policy.get("http_probe_targets", []):
        url = str(item.get("url") or "").strip()
        expected = {int(code) for code in item.get("expected_status") or []}
        if not url or not expected:
            continue
        try:
            if network_container.strip():
                command = [
                    "docker",
                    "exec",
                    network_container.strip(),
                    "python3",
                    "-c",
                    CONTAINER_PROXY_PROBE,
                    proxy_url,
                    url,
                    timeout,
                ]
            else:
                curl_name = shutil.which("curl.exe") or shutil.which("curl")
                if not curl_name:
                    raise RuntimeError("curl is required for proxy probing")
                command = [
                    curl_name,
                    "-s",
                    "-k",
                    "-o",
                    os.devnull,
                    "-w",
                    "%{http_code}",
                    "--max-time",
                    timeout,
                    "-x",
                    proxy_url,
                    url,
                ]
            result = subprocess.run(
                command,
                text=True,
                capture_output=True,
                check=False,
            )
            status_code = int((result.stdout or "0").strip() or "0")
            status_ok = result.returncode == 0 and (
                status_code in expected
                or (
                    accept_status_below_500
                    and 200 <= status_code < 500
                    and status_code != 407
                )
            )
            attempts.append({
                "target": url,
                "status_code": status_code,
                "exit_code": int(result.returncode),
                "ok": status_ok,
                "stderr": (result.stderr or "").strip()[:400],
            })
            if status_ok:
                return {
                    "ok": True,
                    "attempts": attempts,
                    "winning_target": url,
                    "winning_status": status_code,
                }
        except Exception as exc:
            attempts.append({
                "target": url,
                "status_code": 0,
                "ok": False,
                "error": str(exc),
            })
    return {
        "ok": False,
        "attempts": attempts,
    }

def checkout_proxy_lease(base_url: str, management_password: str) -> dict[str, Any]:
    response = requests.post(
        f"{base_url}/proxy/leases/checkout",
        headers=management_headers(management_password, json_content=True),
        json={
            "hostId": "easyproxy-source-audit",
            "providerTypeKey": "easy-proxies",
            "provisionMode": "reuse-only",
            "bindingMode": "shared-instance",
            "metadata": {
                "serviceKey": "easyproxy-source-audit",
                "stage": "availability-audit",
                "purpose": "shared-source-audit",
            },
        },
        timeout=20,
    )
    if response.status_code >= 400:
        body = response.text.strip()
        raise RuntimeError(
            f"proxy lease checkout failed with {response.status_code}: {body or '<empty body>'}"
        )
    payload = response.json()
    result = payload.get("result") or {}
    lease = result.get("lease") or {}
    if not lease or not str(lease.get("id") or "").strip():
        raise RuntimeError("proxy lease checkout returned an empty lease payload")
    return lease

def is_retryable_proxy_lease_error(message: str) -> bool:
    text = str(message or "").strip().lower()
    if not text:
        return False
    return (
        "initial_proxy_probe_pending" in text
        or "timeout waiting for initial probe completion" in text
    )

def report_proxy_lease(base_url: str, lease_id: str, *, management_password: str, success: bool, latency_ms: int = 0, error_code: str = "") -> None:
    response = requests.post(
        f"{base_url}/proxy/leases/report",
        headers=management_headers(management_password, json_content=True),
        json={
            "leaseId": lease_id,
            "success": bool(success),
            "latencyMs": max(0, int(latency_ms)),
            "errorCode": str(error_code or "").strip(),
            "serviceKey": "easyproxy-source-audit",
            "stage": "availability-audit",
            "routeConfidence": "strict",
        },
        timeout=20,
    )
    response.raise_for_status()

def release_proxy_lease(base_url: str, lease_id: str, management_password: str) -> None:
    response = requests.post(
        f"{base_url}/proxy/leases/{lease_id}/release",
        headers=management_headers(management_password),
        timeout=20,
    )
    response.raise_for_status()

def collect_container_networks(container_name: str) -> list[str]:
    result = run(["docker", "inspect", container_name, "--format", "{{json .NetworkSettings.Networks}}"])
    payload = json.loads(result.stdout.strip() or "{}")
    if not isinstance(payload, dict):
        return []
    return sorted(payload.keys())

def normalize_proxy_url_for_host(value: str) -> str:
    text = str(value or "").strip()
    if not text:
        return ""
    return text.replace("://0.0.0.0:", "://127.0.0.1:")

def build_direct_proxy_url(node: dict[str, Any], default_scheme: str = "http") -> str:
    port = int(node.get("port") or 0)
    if port <= 0:
        return ""
    listen_address = str(node.get("listen_address") or "").strip()
    host = "127.0.0.1"
    if listen_address and listen_address not in ("0.0.0.0", "::", "[::]"):
        host = listen_address
    scheme = str(default_scheme or "http").strip() or "http"
    return f"{scheme}://{host}:{port}"

def candidate_priority(node: dict[str, Any]) -> tuple[int, int, int, int]:
    effective = 1 if node.get("effective_available") is True else 0
    available = 1 if node.get("available") is True else 0
    score = int(node.get("availability_score") or 0)
    latency = int(node.get("last_latency_ms") or -1)
    if latency <= 0:
        latency = 1_000_000
    return (
        -effective,
        -available,
        -score,
        latency,
    )

def collect_direct_probe_candidates(nodes: list[dict[str, Any]], *, default_scheme: str = "http") -> list[dict[str, Any]]:
    candidates: list[dict[str, Any]] = []
    for node in nodes:
        if bool(node.get("blacklisted")):
            continue
        proxy_url = build_direct_proxy_url(node, default_scheme=default_scheme)
        if not proxy_url:
            continue
        candidate = dict(node)
        candidate["direct_proxy_url"] = proxy_url
        candidates.append(candidate)
    candidates.sort(key=candidate_priority)
    return candidates

def discover_directly_usable_nodes(
    candidates: list[dict[str, Any]],
    policy: dict[str, Any],
    *,
    network_container: str,
    max_workers: int = 12,
) -> tuple[list[str], list[dict[str, Any]]]:
    if not candidates:
        return [], []

    stable_uris: list[str] = []
    stable_results: list[dict[str, Any]] = []
    retries = max(1, int(policy.get("direct_probe_retries", 1)))
    retry_delay = max(0, int(policy.get("direct_probe_retry_delay_seconds", 1)))

    def probe_candidate(candidate: dict[str, Any]) -> dict[str, Any]:
        last_result: dict[str, Any] = {"ok": False, "attempts": [], "error": "probe not started"}
        for attempt_index in range(retries):
            last_result = probe_http_proxy(
                str(candidate.get("direct_proxy_url") or ""),
                policy,
                network_container=network_container,
            )
            if last_result.get("ok"):
                return last_result
            if attempt_index < retries - 1 and retry_delay > 0:
                time.sleep(retry_delay)
        return last_result

    worker_count = max(1, min(max_workers, len(candidates)))
    with ThreadPoolExecutor(max_workers=worker_count) as executor:
        future_to_candidate = {
            executor.submit(probe_candidate, candidate): candidate
            for candidate in candidates
        }
        for future in as_completed(future_to_candidate):
            candidate = future_to_candidate[future]
            probe_result: dict[str, Any]
            try:
                probe_result = future.result()
            except Exception as exc:
                probe_result = {"ok": False, "attempts": [], "error": str(exc)}

            if not probe_result.get("ok"):
                continue

            uri = str(candidate.get("uri") or "").strip()
            if not uri:
                continue

            stable_uris.append(uri)
            stable_results.append(
                {
                    "tag": str(candidate.get("tag") or ""),
                    "name": str(candidate.get("name") or ""),
                    "source_ref": str(candidate.get("source_ref") or ""),
                    "port": int(candidate.get("port") or 0),
                    "uri": uri,
                    "direct_proxy_url": str(candidate.get("direct_proxy_url") or ""),
                    "effective_available": bool(candidate.get("effective_available") is True),
                    "availability_source": str(candidate.get("availability_source") or ""),
                    "traffic_proven_usable": bool(candidate.get("traffic_proven_usable") is True),
                    "availability_score": int(candidate.get("availability_score") or 0),
                    "last_latency_ms": int(candidate.get("last_latency_ms") or 0),
                    "last_error": str(candidate.get("last_error") or ""),
                    "direct_probe": probe_result,
                }
            )

    stable_uris = sorted(dict.fromkeys(stable_uris))
    stable_results.sort(key=lambda item: (int(item.get("last_latency_ms") or 1_000_000), str(item.get("tag") or "")))
    return stable_uris, stable_results

def stop_container(container_name: str) -> None:
    subprocess.run(["docker", "rm", "-f", container_name], capture_output=True, text=True, check=False)
