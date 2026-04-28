#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
import random
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

import requests
import yaml


REPO_ROOT = Path(__file__).resolve().parent.parent
POLICY_PATH = REPO_ROOT / "shared" / "proxy-availability" / "policy.json"
CURL_IMAGE = os.environ.get("EASYPROXY_AUDIT_CURL_IMAGE", "curlimages/curl:8.12.1")
DEFAULT_CONNECTOR_RUNTIME = {
    "enabled": True,
    "binary_path": "/usr/local/bin/ech-workers",
    "working_directory": "/var/lib/easy-proxy/connectors",
    "listen_host": "127.0.0.1",
    "listen_start_port": 30000,
    "startup_timeout": "30s",
    "preferred_ip": {
        "binary_path": "/usr/local/bin/cfst",
        "ip_file_path": "/usr/local/share/cfst/ip.txt",
        "working_directory": "/var/lib/easy-proxy/connectors/preferred-ip",
        "timeout": "5m0s",
        "fanout_count": 5,
    },
}


def load_policy() -> dict[str, Any]:
    return json.loads(POLICY_PATH.read_text(encoding="utf-8"))


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run a shared EasyProxy-backed availability audit for subscriptions, proxies, and manifest sources.")
    parser.add_argument("--audit-id", default=f"audit-{time.strftime('%Y%m%d-%H%M%S')}")
    parser.add_argument("--image", default="")
    parser.add_argument("--build-if-missing", action="store_true")
    parser.add_argument("--config-path", default="")
    parser.add_argument("--manifest-url", default="")
    parser.add_argument("--manifest-token", default="")
    parser.add_argument("--subscription", action="append", default=[])
    parser.add_argument("--proxy-uri", action="append", default=[])
    parser.add_argument("--fallback-subscription", action="append", default=[])
    parser.add_argument("--connectors-json", default="")
    parser.add_argument("--output-path", default="")
    parser.add_argument("--artifact-dir", default="")
    parser.add_argument("--docker-network-name", default="EasyAiMi")
    parser.add_argument("--scenario-timeout-seconds", type=int, default=0)
    parser.add_argument("--minimum-available-nodes", type=int, default=-1)
    parser.add_argument("--require-manifest-healthy", action="store_true")
    parser.add_argument("--require-fallback-active", action="store_true")
    parser.add_argument("--require-connector-instance-count", type=int, default=0)
    parser.add_argument("--require-stable-node-proxies", type=int, default=1)
    parser.add_argument("--keep-artifacts", action="store_true")
    parser.add_argument("--skip-cleanup", action="store_true")
    return parser.parse_args()


def ensure_docker() -> None:
    shutil.which("docker") or die("docker is required for source audit")


def die(message: str) -> None:
    raise RuntimeError(message)


def run(args: list[str], *, cwd: Path | None = None, capture: bool = True, check: bool = True) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(
        args,
        cwd=str(cwd) if cwd else None,
        text=True,
        capture_output=capture,
        check=False,
    )
    if check and completed.returncode != 0:
        stderr = (completed.stderr or completed.stdout or "").strip()
        raise RuntimeError(f"command failed ({' '.join(args)}): {stderr}")
    return completed


def docker_image_exists(image: str) -> bool:
    result = subprocess.run(
        ["docker", "image", "inspect", image],
        text=True,
        capture_output=True,
        check=False,
    )
    return result.returncode == 0


def ensure_docker_network(name: str) -> None:
    if not name.strip():
        return
    inspect = subprocess.run(["docker", "network", "inspect", name], text=True, capture_output=True, check=False)
    if inspect.returncode == 0:
        return
    created = subprocess.run(["docker", "network", "create", name], text=True, capture_output=True, check=False)
    if created.returncode != 0:
        stderr = (created.stderr or created.stdout or "").strip()
        raise RuntimeError(f"failed to create docker network {name}: {stderr}")


def get_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def is_port_range_available(start: int, size: int) -> bool:
    listeners: list[socket.socket] = []
    try:
        for port in range(start, start + size):
            sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                sock.bind(("127.0.0.1", port))
                listeners.append(sock)
            except OSError:
                sock.close()
                return False
        return True
    finally:
        for sock in listeners:
            sock.close()


def get_free_port_range_start(preferred_start: int, size: int, step: int = 100, max_attempts: int = 200) -> int:
    candidate = preferred_start
    for _ in range(max_attempts):
        if candidate + size - 1 > 65535:
            break
        if is_port_range_available(candidate, size):
            return candidate
        candidate += step
    raise RuntimeError(f"unable to find a free TCP port range near {preferred_start} (size={size})")


def read_json_file(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def write_json_file(path: Path, payload: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")


def normalize_list(values: list[str]) -> list[str]:
    result: list[str] = []
    seen: set[str] = set()
    for value in values:
        item = str(value or "").strip()
        if not item or item in seen:
            continue
        seen.add(item)
        result.append(item)
    return result


def load_connectors(connectors_json: str) -> list[dict[str, Any]]:
    if not connectors_json.strip():
        return []
    payload = json.loads(connectors_json)
    if not isinstance(payload, list):
        raise RuntimeError("--connectors-json must decode to a list")
    return payload


def ensure_image(image: str, build_if_missing: bool, audit_id: str) -> str:
    effective_image = image.strip()
    if not effective_image:
        effective_image = f"easyproxy/source-audit:{audit_id}"
        build_if_missing = True

    if docker_image_exists(effective_image):
        return effective_image

    if not build_if_missing:
        raise RuntimeError(f"docker image does not exist: {effective_image}")

    run(
        [
            "docker",
            "build",
            "-f",
            str(REPO_ROOT / "deploy" / "service" / "base" / "Dockerfile"),
            "-t",
            effective_image,
            str(REPO_ROOT),
        ],
        capture=False,
        check=True,
    )
    return effective_image


def build_config(policy: dict[str, Any], *, manifest_url: str, manifest_token: str, subscriptions: list[str], proxy_uris: list[str], fallback_subscriptions: list[str], connectors: list[dict[str, Any]], multi_port_base: int) -> dict[str, Any]:
    source_sync_enabled = bool(manifest_url.strip())
    management_probe_targets = [
        str(item).strip()
        for item in (policy.get("management_probe_targets") or [])
        if str(item).strip()
    ]
    if not management_probe_targets:
        management_probe_targets = [
            str(item.get("url") or "").strip()
            for item in (policy.get("http_probe_targets") or [])
            if str(item.get("url") or "").strip()
        ]
    config: dict[str, Any] = {
        "mode": "hybrid",
        "log_level": "info",
        "skip_cert_verify": False,
        "database_path": "/var/lib/easy-proxy/data/data.db",
        "listener": {
            "address": "0.0.0.0",
            "port": 22323,
            "protocol": "http",
            "username": "",
            "password": "",
        },
        "pool": {
            "mode": "auto",
            "failure_threshold": 3,
            "blacklist_duration": "24h0m0s",
        },
        "multi_port": {
            "address": "0.0.0.0",
            "base_port": multi_port_base,
            "protocol": "http",
            "username": "",
            "password": "",
        },
        "management": {
            "enabled": True,
            "listen": "0.0.0.0:29888",
            "probe_targets": management_probe_targets,
            "password": "",
        },
        "subscription_refresh": {
            "enabled": True,
            "interval": "2h0m0s",
            "timeout": "30s",
            "health_check_timeout": "1m0s",
            "drain_timeout": "30s",
            "min_available_nodes": max(1, int(policy.get("minimum_available_nodes", 1))),
        },
        "source_sync": {
            "enabled": source_sync_enabled,
            "manifest_url": manifest_url.strip(),
            "manifest_token": manifest_token.strip(),
            "refresh_interval": "5m0s",
            "request_timeout": "15s",
            "default_direct_proxy_scheme": "http",
            "fallback_subscriptions": fallback_subscriptions,
            "connector_runtime": DEFAULT_CONNECTOR_RUNTIME,
        },
        "connectors": connectors,
        "subscriptions": subscriptions,
        "nodes": [
            {
                "name": f"seed-direct-{index + 1}",
                "uri": uri,
            }
            for index, uri in enumerate(proxy_uris)
        ],
    }
    return config


def wait_management_ready(base_url: str, timeout_seconds: int) -> dict[str, Any]:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        try:
            response = requests.get(f"{base_url}/api/settings", timeout=10)
            response.raise_for_status()
            return response.json()
        except Exception:
            time.sleep(3)
    raise RuntimeError(f"timed out waiting for management API at {base_url}")


def wait_scenario_state(base_url: str, timeout_seconds: int, require_manifest_healthy: bool, require_fallback_active: bool, require_connector_instances: int) -> tuple[dict[str, Any], dict[str, Any] | None]:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        try:
            nodes_response = requests.get(f"{base_url}/api/nodes", timeout=15)
            nodes_response.raise_for_status()
            nodes = nodes_response.json()
            source_sync = None
            try:
                source_sync_response = requests.get(f"{base_url}/api/source-sync/status", timeout=10)
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


def fetch_nodes_and_source_sync(base_url: str) -> tuple[dict[str, Any], dict[str, Any] | None]:
    nodes_response = requests.get(f"{base_url}/api/nodes", timeout=15)
    nodes_response.raise_for_status()
    nodes = nodes_response.json()
    source_sync = None
    try:
        source_sync_response = requests.get(f"{base_url}/api/source-sync/status", timeout=10)
        source_sync_response.raise_for_status()
        source_sync = source_sync_response.json()
    except Exception:
        source_sync = None
    return nodes, source_sync


def probe_http_proxy(proxy_url: str, policy: dict[str, Any], *, network_container: str = "") -> dict[str, Any]:
    attempts: list[dict[str, Any]] = []
    timeout = str(int(policy.get("request_timeout_seconds", 25)))
    for item in policy.get("http_probe_targets", []):
        url = str(item.get("url") or "").strip()
        expected = {int(code) for code in item.get("expected_status") or []}
        if not url or not expected:
            continue
        try:
            if network_container.strip():
                command = [
                    "docker",
                    "run",
                    "--rm",
                    "--network",
                    f"container:{network_container.strip()}",
                    CURL_IMAGE,
                    "-s",
                    "-k",
                    "-o",
                    "/dev/null",
                    "-w",
                    "%{http_code}",
                    "--max-time",
                    timeout,
                    "-x",
                    proxy_url,
                    url,
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
            attempts.append({
                "target": url,
                "status_code": status_code,
                "exit_code": int(result.returncode),
                "ok": result.returncode == 0 and status_code in expected,
                "stderr": (result.stderr or "").strip()[:400],
            })
            if result.returncode == 0 and status_code in expected:
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


def compat_headers() -> dict[str, str]:
    return {
        "Content-Type": "application/json",
    }


def checkout_proxy_lease(base_url: str) -> dict[str, Any]:
    response = requests.post(
        f"{base_url}/proxy/leases/checkout",
        headers=compat_headers(),
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


def report_proxy_lease(base_url: str, lease_id: str, *, success: bool, latency_ms: int = 0, error_code: str = "") -> None:
    response = requests.post(
        f"{base_url}/proxy/leases/report",
        headers=compat_headers(),
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


def release_proxy_lease(base_url: str, lease_id: str) -> None:
    response = requests.post(
        f"{base_url}/proxy/leases/{lease_id}/release",
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


def stop_container(container_name: str) -> None:
    subprocess.run(["docker", "rm", "-f", container_name], capture_output=True, text=True, check=False)


def main() -> int:
    args = parse_args()
    ensure_docker()
    policy = load_policy()

    subscriptions = normalize_list(args.subscription)
    proxy_uris = normalize_list(args.proxy_uri)
    fallback_subscriptions = normalize_list(args.fallback_subscription)
    connectors = load_connectors(args.connectors_json)

    if not subscriptions and not proxy_uris and not connectors and not args.manifest_url.strip():
        raise RuntimeError("at least one of --subscription, --proxy-uri, --connectors-json, or --manifest-url is required")

    scenario_timeout = args.scenario_timeout_seconds or int(policy.get("scenario_timeout_seconds", 720))
    minimum_available_nodes = args.minimum_available_nodes if args.minimum_available_nodes >= 0 else int(policy.get("minimum_available_nodes", 1))

    artifact_dir = Path(args.artifact_dir) if args.artifact_dir.strip() else REPO_ROOT / "tmp" / "easy-proxy-source-audit" / args.audit_id
    artifact_dir.mkdir(parents=True, exist_ok=True)
    config_path = artifact_dir / "config.yaml"
    data_dir = artifact_dir / "data"
    data_dir.mkdir(parents=True, exist_ok=True)

    effective_image = ensure_image(args.image, args.build_if_missing, args.audit_id)
    multi_port_base = get_free_port_range_start(34000 + random.randint(0, 20) * 100, 81)
    config_payload = build_config(
        policy,
        manifest_url=args.manifest_url,
        manifest_token=args.manifest_token,
        subscriptions=subscriptions,
        proxy_uris=proxy_uris,
        fallback_subscriptions=fallback_subscriptions,
        connectors=connectors,
        multi_port_base=multi_port_base,
    )
    config_path.write_text(yaml.safe_dump(config_payload, sort_keys=False, allow_unicode=True), encoding="utf-8")

    management_port = get_free_port()
    pool_port = get_free_port()
    container_name = f"easyproxy-source-audit-{args.audit_id}".lower().replace("_", "-")
    stop_container(container_name)

    if args.docker_network_name.strip():
        ensure_docker_network(args.docker_network_name.strip())

    docker_args = [
        "docker",
        "run",
        "-d",
        "--name",
        container_name,
        "-p",
        f"{management_port}:29888",
        "-p",
        f"{pool_port}:22323",
        "-v",
        f"{config_path.resolve()}:/etc/easy-proxy/config.yaml",
        "-v",
        f"{data_dir.resolve()}:/var/lib/easy-proxy",
    ]
    if args.docker_network_name.strip():
        docker_args.extend(["--network", args.docker_network_name.strip()])
    docker_args.append(effective_image)
    run(docker_args, capture=False, check=True)

    summary_path = Path(args.output_path) if args.output_path.strip() else artifact_dir / "summary.json"
    try:
        base_url = f"http://127.0.0.1:{management_port}"
        wait_management_ready(base_url, 180)
        wait_scenario_state(
            base_url,
            scenario_timeout,
            require_manifest_healthy=args.require_manifest_healthy,
            require_fallback_active=args.require_fallback_active,
            require_connector_instances=args.require_connector_instance_count,
        )
        probe_deadline = time.time() + scenario_timeout
        last_nodes: dict[str, Any] = {}
        last_source_sync: dict[str, Any] | None = None
        last_pool_probe: dict[str, Any] = {"ok": False, "attempts": []}
        last_best_proxy_payload: dict[str, Any] = {}
        best_proxy_probe: dict[str, Any] = {"ok": False, "attempts": []}
        compat_probe: dict[str, Any] = {"ok": False, "attempts": []}
        stable_results: list[dict[str, Any]] = []
        stable_uris: list[str] = []
        container_networks = collect_container_networks(container_name)

        while time.time() < probe_deadline:
            last_nodes, last_source_sync = fetch_nodes_and_source_sync(base_url)
            all_nodes = list(last_nodes.get("nodes") or [])
            candidate_nodes = [item for item in all_nodes if item.get("effective_available") is True]
            if not candidate_nodes:
                candidate_nodes = [item for item in all_nodes if item.get("available") is True]
            stable_results = []
            stable_uris = []
            for node in candidate_nodes:
                uri = str(node.get("uri") or "").strip()
                stable_results.append({
                    "tag": str(node.get("tag") or ""),
                    "name": str(node.get("name") or ""),
                    "port": int(node.get("port") or 0),
                    "uri": uri,
                    "effective_available": bool(node.get("effective_available") is True),
                    "availability_source": str(node.get("availability_source") or ""),
                    "traffic_proven_usable": bool(node.get("traffic_proven_usable") is True),
                    "availability_score": int(node.get("availability_score") or 0),
                    "last_latency_ms": int(node.get("last_latency_ms") or 0),
                    "last_error": str(node.get("last_error") or ""),
                })
                if uri:
                    stable_uris.append(uri)

            last_pool_probe = probe_http_proxy("http://127.0.0.1:22323", policy, network_container=container_name)
            try:
                best_proxy_response = requests.get(f"{base_url}/api/best-proxy?top=3", timeout=20)
                best_proxy_response.raise_for_status()
                last_best_proxy_payload = best_proxy_response.json()
            except Exception:
                last_best_proxy_payload = {}

            best_proxy_probe = {"ok": False, "attempts": []}
            for candidate in list((last_best_proxy_payload.get("nodes") or []))[:3]:
                proxy_url = normalize_proxy_url_for_host(candidate.get("proxy_url"))
                if not proxy_url:
                    continue
                result = probe_http_proxy(proxy_url, policy, network_container=container_name)
                best_proxy_probe["attempts"].append({
                    "tag": str(candidate.get("tag") or ""),
                    "name": str(candidate.get("name") or ""),
                    "proxy_url": proxy_url,
                    "probe": result,
                })
                if result["ok"]:
                    best_proxy_probe.update({
                        "ok": True,
                        "selected_tag": str(candidate.get("tag") or ""),
                        "selected_name": str(candidate.get("name") or ""),
                        "selected_proxy_url": proxy_url,
                    })
                    break

            compat_probe = {"ok": False, "attempts": []}
            if stable_results:
                lease_attempts = max(1, min(len(stable_results), 12))
                for _ in range(lease_attempts):
                    try:
                        lease = checkout_proxy_lease(base_url)
                    except Exception as exc:
                        compat_probe["attempts"].append({
                            "lease_id": "",
                            "selected_tag": "",
                            "proxy_url": "",
                            "probe": {"ok": False, "attempts": [], "error": str(exc)},
                        })
                        break
                    lease_id = str(lease.get("id") or "").strip()
                    proxy_url = normalize_proxy_url_for_host(str(lease.get("proxyUrl") or "").strip())
                    selected_tag = str((lease.get("metadata") or {}).get("selectedNodeTag") or "")
                    result = probe_http_proxy(proxy_url, policy, network_container=container_name)
                    compat_probe["attempts"].append({
                        "lease_id": lease_id,
                        "selected_tag": selected_tag,
                        "proxy_url": proxy_url,
                        "probe": result,
                    })
                    try:
                        if result["ok"]:
                            latency_values = [
                                int(item.get("last_latency_ms") or 0)
                                for item in stable_results
                                if str(item.get("tag") or "") == selected_tag and int(item.get("last_latency_ms") or 0) > 0
                            ]
                            report_proxy_lease(
                                base_url,
                                lease_id,
                                success=True,
                                latency_ms=latency_values[0] if latency_values else 0,
                            )
                            compat_probe.update({
                                "ok": True,
                                "selected_tag": selected_tag,
                                "selected_proxy_url": proxy_url,
                                "lease_id": lease_id,
                            })
                            break
                        failure_codes = [
                            f"{item.get('target')}:{item.get('status_code')}"
                            for item in result.get("attempts") or []
                            if not item.get("ok")
                        ]
                        report_proxy_lease(
                            base_url,
                            lease_id,
                            success=False,
                            error_code="runtime-audit:" + ("|".join(failure_codes)[:200] if failure_codes else "probe-failed"),
                        )
                    finally:
                        try:
                            release_proxy_lease(base_url, lease_id)
                        except Exception:
                            pass

            if (
                compat_probe["ok"]
                and len(stable_uris) >= minimum_available_nodes
                and len(stable_uris) >= args.require_stable_node_proxies
            ):
                break
            time.sleep(8)

        if not compat_probe["ok"]:
            raise RuntimeError("proxy lease output failed across all shared probe targets")
        if len(stable_uris) < minimum_available_nodes:
            raise RuntimeError(
                f"stable direct proxy count {len(stable_uris)} is lower than required minimum available nodes {minimum_available_nodes}"
            )
        if args.require_stable_node_proxies > 0 and len(stable_uris) < args.require_stable_node_proxies:
            raise RuntimeError(
                f"stable direct proxy count {len(stable_uris)} is lower than required {args.require_stable_node_proxies}"
            )
        if args.docker_network_name.strip() and args.docker_network_name.strip() not in container_networks:
            raise RuntimeError(
                f"container did not join expected docker network {args.docker_network_name.strip()} (actual: {container_networks})"
            )

        payload = {
            "audit_id": args.audit_id,
            "validated_image": effective_image,
            "artifact_dir": str(artifact_dir),
            "config_path": str(config_path),
            "docker_networks": container_networks,
            "inputs": {
                "subscriptions": subscriptions,
                "proxy_uris": proxy_uris,
                "fallback_subscriptions": fallback_subscriptions,
                "manifest_url": args.manifest_url.strip(),
                "connector_count": len(connectors),
            },
            "nodes": {
                "total_nodes": int(last_nodes.get("total_nodes") or 0),
                "available_nodes": int(last_nodes.get("available_nodes") or 0),
                "available_preview": [
                    {
                        "tag": str(item.get("tag") or ""),
                        "name": str(item.get("name") or ""),
                        "uri": str(item.get("uri") or ""),
                    }
                    for item in [node for node in (last_nodes.get("nodes") or []) if node.get("available") is True][:20]
                ],
                "stable_available_uris": sorted(dict.fromkeys(stable_uris)),
                "stable_probe_results": stable_results,
            },
            "pool_probe": last_pool_probe,
            "best_proxy": last_best_proxy_payload,
            "best_proxy_probe": best_proxy_probe,
            "compat_probe": compat_probe,
            "source_sync": last_source_sync or {},
        }
        write_json_file(summary_path, payload)
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return 0
    except Exception as exc:
        debug_payload = {
            "audit_id": args.audit_id,
            "validated_image": effective_image,
            "artifact_dir": str(artifact_dir),
            "config_path": str(config_path),
            "container_name": container_name,
            "management_base_url": f"http://127.0.0.1:{management_port}",
            "pool_proxy_url": f"http://127.0.0.1:{pool_port}",
            "docker_network_name": args.docker_network_name.strip(),
            "docker_networks": collect_container_networks(container_name),
            "nodes": {
                "total_nodes": int((last_nodes or {}).get("total_nodes") or 0) if 'last_nodes' in locals() else 0,
                "available_nodes": int((last_nodes or {}).get("available_nodes") or 0) if 'last_nodes' in locals() else 0,
                "stable_available_uris": sorted(dict.fromkeys(stable_uris)) if 'stable_uris' in locals() else [],
                "stable_probe_results": stable_results if 'stable_results' in locals() else [],
            },
            "pool_probe": last_pool_probe if 'last_pool_probe' in locals() else {},
            "best_proxy": last_best_proxy_payload if 'last_best_proxy_payload' in locals() else {},
            "best_proxy_probe": best_proxy_probe if 'best_proxy_probe' in locals() else {},
            "compat_probe": compat_probe if 'compat_probe' in locals() else {},
            "source_sync": last_source_sync if 'last_source_sync' in locals() else {},
            "error": str(exc),
        }
        try:
            write_json_file(summary_path, debug_payload)
        except Exception:
            pass
        logs_path = artifact_dir / "docker.log"
        try:
            logs = run(["docker", "logs", container_name], capture=True, check=False)
            logs_path.write_text((logs.stdout or "") + (logs.stderr or ""), encoding="utf-8")
        except Exception:
            pass
        raise
    finally:
        if not args.skip_cleanup:
            stop_container(container_name)
        if not args.keep_artifacts:
            # Keep summary/config if output was explicitly requested, otherwise allow cleanup of transient docker data only.
            pass


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
