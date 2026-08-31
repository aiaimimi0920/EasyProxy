from __future__ import annotations

import json
import os
import shutil
import socket
import subprocess
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parent.parent
POLICY_PATH = REPO_ROOT / "shared" / "proxy-availability" / "policy.json"
CURL_IMAGE = os.environ.get("EASYPROXY_AUDIT_CURL_IMAGE", "curlimages/curl:8.12.1")
DEFAULT_CONNECTOR_RUNTIME = {
    "enabled": True,
    "binary_path": "/usr/local/bin/ech-workers",
    "working_directory": "/var/lib/easyproxy/connectors",
    "listen_host": "127.0.0.1",
    "listen_start_port": 30000,
    "startup_timeout": "30s",
    "preferred_ip": {
        "binary_path": "/usr/local/bin/cfst",
        "ip_file_path": "/usr/share/easyproxy/cfst/ip.txt",
        "working_directory": "/var/lib/easyproxy/connectors/preferred-ip",
        "timeout": "5m0s",
        "fanout_count": 5,
    },
}

def load_policy() -> dict[str, Any]:
    return json.loads(POLICY_PATH.read_text(encoding="utf-8"))

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
        raise RuntimeError("EASYPROXY_AUDIT_CONNECTORS_JSON must decode to a list")
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

def build_config(
    policy: dict[str, Any],
    *,
    manifest_url: str,
    manifest_token: str,
    management_password: str,
    subscriptions: list[str],
    proxy_uris: list[str],
    fallback_subscriptions: list[str],
    detour_source_refs: list[str],
    connectors: list[dict[str, Any]],
    multi_port_base: int,
) -> dict[str, Any]:
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
        "database_path": "/var/lib/easyproxy/data/data.db",
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
            "detour_source_refs": detour_source_refs,
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
            "password": management_password,
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
