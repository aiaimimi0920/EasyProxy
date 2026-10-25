"""批准根的应用编排入口；verify 分支先返回，永不读取写凭据。"""
from __future__ import annotations

from pathlib import Path

from aggregator_build import SEED_SECRETS, build_aggregator
from audit_image import build_image, build_runtime
from deploy import deploy_suite
from destroy_preview import destroy_preview
from migration_prepare import prepare_migration
from preview import preview_suite
from refresh_context import require
from request import ApplicationRequest
from sealed_box import build_sealer
from sources import inspect_checkout
from toolchain import build_cli, prepare_linux_tools
from verify_public import verify_public


def execute(request: ApplicationRequest, root: Path, output: Path, *, environment) -> dict:
    require(type(request) is ApplicationRequest)
    inspect_checkout(root, request.config.repository, request.source_sha)
    if request.action == "verify":
        # In particular do not call ApplicationRequest.credentials on the
        # platform's verification-not-applicable placeholders.
        verified = verify_public(request.config, request.candidate, root)
        return {"checks": verified["checks"]}
    values = request.credentials(environment)
    if request.action == "destroy-preview":
        return destroy_preview(request, root, output, credentials=values)
    require(request.action in ("deploy", "update", "preview"))
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and root != output and root not in output.parents)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    tools = prepare_linux_tools(output / "tools")
    runtime = build_runtime(root, request.config.repository, request.source_sha, tools, output / "runtime")
    image = build_image(runtime, output / "image")
    sealer = build_sealer(root, request.config.repository, request.source_sha, tools, output / "sealer")
    bodies = build_aggregator(root, request.config.repository, request.source_sha, output / "generation",
                              seed_secrets={key: values[key] for key in SEED_SECRETS})
    if request.action == "preview":
        return preview_suite(request, root, tools, image, sealer, bodies, output / "publish", credentials=values)
    migration = None
    if request.action == "update":
        executable = build_cli(root, request.config.repository, request.source_sha, tools, output / "cli")
        migration = prepare_migration(request.config, request.candidate, root, tools, executable, output / "migration")
    return deploy_suite(request, root, tools, image, sealer, bodies, output / "publish",
                        credentials=values, migration_settings=migration)
