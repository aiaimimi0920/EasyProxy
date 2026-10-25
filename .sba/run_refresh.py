"""只从已提交实例批准源码启动真实周期刷新；不接收任意版本覆盖参数。"""
from __future__ import annotations

import argparse
import os
from pathlib import Path

from aggregator_build import SEED_SECRETS, build_aggregator
from audit_image import build_image, build_runtime
from deadline import action_deadline
from refresh import refresh_suite
from refresh_context import context, credentials, manifest_release, object_json, require
from sources import inspect_checkout
from toolchain import prepare_linux_tools


def execute(selection: Path, output: Path, *, environment):
    config = context(environment)
    values = credentials(environment)
    require(selection.is_absolute() and selection.resolve(strict=True) == selection and selection.is_file()
            and not selection.is_symlink() and selection.stat().st_size <= 49152)
    chosen = object_json(selection.read_text(encoding="utf-8"))
    require(set(chosen) == {"schemaVersion", "configuration", "environment", "release"}
            and type(chosen["schemaVersion"]) is int and chosen["schemaVersion"] == 1
            and chosen["configuration"] == config.configuration() and chosen["environment"] == config.environment)
    root = Path(__file__).resolve().parents[1]
    release = chosen["release"]
    require(type(release) is dict and manifest_release(root, release.get("sourceSha")) == release)
    inspect_checkout(root, config.repository, release["sourceSha"])
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and root != output and root not in output.parents)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    tools = prepare_linux_tools(output / "tools")
    runtime = build_runtime(root, config.repository, release["sourceSha"], tools, output / "runtime")
    image = build_image(runtime, output / "image")
    bodies = build_aggregator(root, config.repository, release["sourceSha"], output / "generation",
                              seed_secrets={key: values[key] for key in SEED_SECRETS})
    return refresh_suite(config, release, root, image, bodies, output / "publish", credentials=values)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--selection", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--deadline", type=float, required=True)
    args = parser.parse_args()
    try:
        with action_deadline(args.deadline):
            execute(args.selection, args.output, environment=os.environ)
    except Exception:
        raise SystemExit("EASYPROXY_SBA_REFRESH_FAILED_OR_UNKNOWN") from None
    print("EASYPROXY_SBA_REFRESH_VERIFIED")


if __name__ == "__main__":
    main()
