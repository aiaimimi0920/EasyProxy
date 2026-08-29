#!/usr/bin/env python3

from __future__ import annotations

import argparse
import json
import os
import random
import sys
import time
from pathlib import Path
from typing import Any

import requests
import yaml

try:
    from .easyproxy_source_audit_probe import (
        checkout_proxy_lease,
        collect_container_networks,
        collect_direct_probe_candidates,
        discover_directly_usable_nodes,
        fetch_nodes_and_source_sync,
        is_retryable_proxy_lease_error,
        normalize_proxy_url_for_host,
        probe_http_proxy,
        release_proxy_lease,
        report_proxy_lease,
        stop_container,
        wait_management_ready,
        wait_scenario_state,
    )
    from .easyproxy_source_audit_support import (
        REPO_ROOT,
        build_config,
        ensure_docker,
        ensure_docker_network,
        ensure_image,
        get_free_port,
        get_free_port_range_start,
        load_connectors,
        load_policy,
        normalize_list,
        run,
        write_json_file,
    )
except ImportError:
    from easyproxy_source_audit_probe import (
        checkout_proxy_lease,
        collect_container_networks,
        collect_direct_probe_candidates,
        discover_directly_usable_nodes,
        fetch_nodes_and_source_sync,
        is_retryable_proxy_lease_error,
        normalize_proxy_url_for_host,
        probe_http_proxy,
        release_proxy_lease,
        report_proxy_lease,
        stop_container,
        wait_management_ready,
        wait_scenario_state,
    )
    from easyproxy_source_audit_support import (
        REPO_ROOT,
        build_config,
        ensure_docker,
        ensure_docker_network,
        ensure_image,
        get_free_port,
        get_free_port_range_start,
        load_connectors,
        load_policy,
        normalize_list,
        run,
        write_json_file,
    )

def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run a shared EasyProxy-backed availability audit for subscriptions, proxies, and manifest sources.")
    parser.add_argument("--audit-id", default=f"audit-{time.strftime('%Y%m%d-%H%M%S')}")
    parser.add_argument("--image", default="")
    parser.add_argument("--build-if-missing", action="store_true")
    parser.add_argument("--config-path", default="")
    parser.add_argument("--manifest-url", default="")
    parser.add_argument("--subscription", action="append", default=[])
    parser.add_argument("--proxy-uri", action="append", default=[])
    parser.add_argument("--fallback-subscription", action="append", default=[])
    parser.add_argument("--output-path", default="")
    parser.add_argument("--artifact-dir", default="")
    parser.add_argument("--docker-network-name", default="EasyAiMi")
    parser.add_argument("--dns-server", action="append", default=[])
    parser.add_argument("--scenario-timeout-seconds", type=int, default=0)
    parser.add_argument("--minimum-available-nodes", type=int, default=-1)
    parser.add_argument("--require-manifest-healthy", action="store_true")
    parser.add_argument("--require-fallback-active", action="store_true")
    parser.add_argument("--require-connector-instance-count", type=int, default=0)
    parser.add_argument("--require-stable-node-proxies", type=int, default=1)
    parser.add_argument("--keep-artifacts", action="store_true")
    parser.add_argument("--skip-cleanup", action="store_true")
    return parser.parse_args()

def main() -> int:
    args = parse_args()
    ensure_docker()
    policy = load_policy()

    subscriptions = normalize_list(args.subscription)
    proxy_uris = normalize_list(args.proxy_uri)
    fallback_subscriptions = normalize_list(args.fallback_subscription)
    dns_servers = normalize_list(args.dns_server)
    manifest_token = os.environ.get("EASYPROXY_AUDIT_MANIFEST_TOKEN", "")
    connectors_json = os.environ.get("EASYPROXY_AUDIT_CONNECTORS_JSON", "")
    connectors = load_connectors(connectors_json)

    if not subscriptions and not proxy_uris and not connectors and not args.manifest_url.strip():
        raise RuntimeError(
            "at least one subscription, proxy URI, connector environment payload, or manifest URL is required"
        )

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
        manifest_token=manifest_token,
        subscriptions=subscriptions,
        proxy_uris=proxy_uris,
        fallback_subscriptions=fallback_subscriptions,
        connectors=connectors,
        multi_port_base=multi_port_base,
    )
    config_fd = os.open(config_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(config_fd, "w", encoding="utf-8") as config_file:
        config_file.write(yaml.safe_dump(config_payload, sort_keys=False, allow_unicode=True))

    management_port = get_free_port()
    pool_port = get_free_port()
    container_name = f"easyproxy-source-audit-{args.audit_id}".lower().replace("_", "-")
    stop_container(container_name)

    try:
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
            "--env",
            "EASY_PROXY_RUN_AS_ROOT=1",
            "-v",
            f"{data_dir.resolve()}:/var/lib/easyproxy",
            "-v",
            f"{config_path.resolve()}:/var/lib/easyproxy/config/config.yaml",
        ]
        if args.docker_network_name.strip():
            docker_args.extend(["--network", args.docker_network_name.strip()])
        for dns_server in dns_servers:
            docker_args.extend(["--dns", dns_server])
        docker_args.append(effective_image)
        run(docker_args, capture=False, check=True)
    except Exception:
        config_path.unlink(missing_ok=True)
        raise

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
            candidate_nodes = collect_direct_probe_candidates(all_nodes, default_scheme="http")
            stable_uris, stable_results = discover_directly_usable_nodes(
                candidate_nodes,
                policy,
                network_container=container_name,
            )

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
                        if is_retryable_proxy_lease_error(str(exc)):
                            time.sleep(5)
                            continue
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
            "dns_servers": dns_servers,
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
            "dns_servers": dns_servers,
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
        config_path.unlink(missing_ok=True)
        if not args.skip_cleanup:
            stop_container(container_name)
        if not args.keep_artifacts:
            # The secret-bearing config is always removed; other diagnostics may remain.
            pass

if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - CLI failure path
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
