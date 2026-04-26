#!/usr/bin/env python3

from __future__ import annotations

import argparse
import copy
import json
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


def render_service_config(root: dict[str, Any], output: Path) -> None:
    template = yaml.safe_load(SERVICE_TEMPLATE_PATH.read_text(encoding="utf-8")) or {}
    service_root = as_dict(root.get("serviceBase"))
    runtime_overlay = as_dict(service_root.get("runtime"))
    config = deep_merge(template, runtime_overlay)

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
