#!/usr/bin/env python3

from __future__ import annotations

import argparse
import copy
import json
import re
from pathlib import Path
from typing import Any

import yaml


REPO_ROOT = Path(__file__).resolve().parent.parent
SERVICE_TEMPLATE_PATH = REPO_ROOT / "deploy" / "service" / "base" / "config.template.yaml"


def deep_merge(base: Any, overlay: Any) -> Any:
    if overlay is None:
        return copy.deepcopy(base)
    if isinstance(base, dict) and isinstance(overlay, dict):
        merged = copy.deepcopy(base)
        for key, value in overlay.items():
            if key in merged:
                merged[key] = deep_merge(merged[key], value)
            else:
                merged[key] = copy.deepcopy(value)
        return merged
    return copy.deepcopy(overlay)


def as_dict(value: Any) -> dict[str, Any]:
    return value if isinstance(value, dict) else {}


def normalize_env_mapping(value: Any) -> dict[str, str]:
    result: dict[str, str] = {}
    for key, item in as_dict(value).items():
        if item is None:
            result[str(key)] = ""
        else:
            result[str(key)] = str(item)
    return result


def normalize_local_server_runtime(config: dict[str, Any]) -> None:
    raw_local = config.get("local_server")
    if raw_local is None:
        return
    if not isinstance(raw_local, dict):
        raise ValueError("local_server must be a mapping")

    local = raw_local
    if not local.get("enabled"):
        return
    if config.get("mode") != "pool":
        raise ValueError("local_server requires mode: pool")
    if config.get("extra_listeners"):
        raise ValueError("local_server does not allow extra_listeners")

    listener = config.setdefault("listener", {})
    if not isinstance(listener, dict) or listener.get("protocol") != "mixed":
        raise ValueError("local_server requires listener.protocol: mixed")

    auth = local.get("auth") or {}
    if not isinstance(auth, dict):
        raise ValueError("local_server.auth must be a mapping")
    username = str(auth.get("username") or "").strip()
    password = str(auth.get("password") or "")
    if not re.fullmatch(r"[A-Za-z0-9._-]{1,64}", username):
        raise ValueError("local_server canonical username is invalid")
    if (
        not password
        or "change_me" in password
        or "\x00" in password
        or len(password.encode("utf-8")) > 256
    ):
        raise ValueError("local_server canonical credential is unresolved")

    routing = config.get("routing") or {}
    if not isinstance(routing, dict):
        routing = {}
    routing_listen = str(routing.get("listen") or "").strip()
    local_listen = str(local.get("listen") or "").strip()
    if local_listen and routing_listen and local_listen != routing_listen:
        raise ValueError("local_server.listen conflicts with routing.listen")

    listener["username"] = username
    listener["password"] = password
    management = config.setdefault("management", {})
    if not isinstance(management, dict):
        management = {}
        config["management"] = management
    management["password"] = password
    local.setdefault("shared_revision", 1)
    local.setdefault("credential_generation", 2)


def render_service_config(root: dict[str, Any], output: Path) -> None:
    template = yaml.safe_load(SERVICE_TEMPLATE_PATH.read_text(encoding="utf-8")) or {}
    service_root = as_dict(root.get("serviceBase"))
    runtime_overlay = as_dict(service_root.get("runtime"))
    config = deep_merge(template, runtime_overlay)
    normalize_local_server_runtime(config)

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(
        yaml.safe_dump(config, sort_keys=False, allow_unicode=True),
        encoding="utf-8",
    )


def render_misub_env(root: dict[str, Any], output: Path) -> None:
    misub = as_dict(root.get("misub"))
    docker = as_dict(misub.get("docker"))
    env_map = normalize_env_mapping(docker.get("env"))

    lines = [
        "# Generated from root config.yaml",
    ]
    for key, value in env_map.items():
        lines.append(f"{key}={value}")

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text("\n".join(lines).rstrip() + "\n", encoding="utf-8")


def render_worker_devvars(root: dict[str, Any], output: Path) -> None:
    worker = as_dict(root.get("echWorkersCloudflare"))
    secrets = normalize_env_mapping(worker.get("secrets"))

    lines = [
        "# Generated from root config.yaml",
    ]
    for key, value in secrets.items():
        lines.append(f"{key}={json.dumps(value, ensure_ascii=False)}")

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text("\n".join(lines).rstrip() + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description="Render derived EasyProxy config files from the root config.yaml.")
    parser.add_argument("--root-config", default=str(REPO_ROOT / "config.yaml"))
    parser.add_argument("--service-output", default="")
    parser.add_argument("--misub-env-output", default="")
    parser.add_argument("--worker-devvars-output", default="")
    args = parser.parse_args()

    root_config_path = Path(args.root_config)
    if not root_config_path.exists():
        raise SystemExit(f"Root config not found: {root_config_path}")

    root = yaml.safe_load(root_config_path.read_text(encoding="utf-8")) or {}

    if args.service_output:
        render_service_config(root, Path(args.service_output))
        print(f"Rendered service config -> {args.service_output}")

    if args.misub_env_output:
        render_misub_env(root, Path(args.misub_env_output))
        print(f"Rendered MiSub env -> {args.misub_env_output}")

    if args.worker_devvars_output:
        render_worker_devvars(root, Path(args.worker_devvars_output))
        print(f"Rendered worker .dev.vars -> {args.worker_devvars_output}")

    if not args.service_output and not args.misub_env_output and not args.worker_devvars_output:
        print("Nothing to render. Pass at least one output flag.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
