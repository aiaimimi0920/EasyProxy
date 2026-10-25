"""默认分支只选已提交 active SHA；不获取 writer，不运行默认分支的发布代码。"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from coordination import Coordinator, r2_store
from refresh_context import context, credentials, manifest_release, require
from sources import materialize_workspace
from suite_config import WORKFLOW


def select_source(dispatch: Path, output: Path, *, environment) -> dict:
    config = context(environment)  # Must reject preview before reading inherited secrets.
    values = credentials(environment)
    require(dispatch.is_absolute() and dispatch.resolve(strict=True) == dispatch
            and output.is_absolute() and output.resolve() == output and not output.exists()
            and dispatch != output and dispatch not in output.parents)
    store = r2_store(config.account_id, config.bucket, values["EASYPROXY_R2_ACCESS_KEY_ID"],
                     values["EASYPROXY_R2_SECRET_ACCESS_KEY"])
    try:
        owner = Coordinator(store, config.identity("production"), config.inventory_digest)
        release = owner.current_release()
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        root = output / "source"
        materialize_workspace(root, config.repository, release["sourceSha"], cache_root=dispatch)
        require(manifest_release(root, release["sourceSha"]) == release)
        # A changed scheduler must not silently execute an older, incompatible protocol.
        require((root / WORKFLOW).read_bytes() == (dispatch / WORKFLOW).read_bytes())
        require(owner.current_release() == release)
        result = {"schemaVersion": 1, "configuration": config.configuration(), "environment": config.environment,
                  "release": release}
        with (output / "selection.json").open("x", encoding="utf-8", newline="\n") as stream:
            json.dump(result, stream, sort_keys=True, separators=(",", ":"))
        return result
    finally:
        store.client.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        select_source(Path(__file__).resolve().parents[1], args.output, environment=os.environ)
    except Exception:
        raise SystemExit("EASYPROXY_SBA_REFRESH_SELECTION_FAILED") from None


if __name__ == "__main__":
    main()
