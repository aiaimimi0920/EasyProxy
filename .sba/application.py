"""批准根的应用编排入口；verify 分支先返回，永不读取写凭据。"""
from __future__ import annotations

from pathlib import Path
from dataclasses import replace

from aggregator_build import SEED_SECRETS, build_aggregator
from audit_image import build_image, build_runtime
from deploy import deploy_suite
from destroy_preview import destroy_preview
from diagnostics import phase
from fresh_pristine_repair import assert_fresh_pristine
from migration_prepare import prepare_migration
from preview import preview_suite
from pristine_repair import assert_pristine
from refresh_context import require
from request import ApplicationRequest
from repair_context import REPAIR_ID, GITHUB_REPAIR_ID, PAGES_REPAIR_ID, ASSETS_REPAIR_ID
from repair_context import GENERATION_REPAIR_ID, PAGES_DIAGNOSTIC_REPAIR_ID
from repair_context import GENERATION_OUTPUTS_REPAIR_ID, PAGES_ASSET_PROTOCOL_REPAIR_ID
from repair_context import FRESH_PRISTINE_REPAIR_ID
from sealed_box import build_sealer
from sources import inspect_checkout
from toolchain import build_cli, prepare_linux_tools
from verify_public import verify_public


def execute(request: ApplicationRequest, root: Path, output: Path, *, environment) -> dict:
    require(type(request) is ApplicationRequest)
    with phase("SOURCE"):
        inspect_checkout(root, request.config.repository, request.source_sha)
    if request.action == "verify":
        # In particular do not call ApplicationRequest.credentials on the
        # platform's verification-not-applicable placeholders.
        with phase("VERIFY"):
            verified = verify_public(request.config, request.candidate, root)
        return {"checks": verified["checks"]}
    with phase("CREDENTIALS"):
        values = request.credentials(environment)
    if request.action == "destroy-preview":
        with phase("DESTROY_PREVIEW"):
            return destroy_preview(request, root, output, credentials=values)
    require(request.action in ("deploy", "update", "preview", "repair"))
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and root != output and root not in output.parents)
    with phase("OUTPUT"):
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
    if request.action == "repair" and request.context.get("repairId") == REPAIR_ID:
        with phase("PRISTINE"):
            assert_pristine(request, values)
    fresh_pristine = request.action == "repair" and request.context.get("repairId") == FRESH_PRISTINE_REPAIR_ID
    if fresh_pristine:
        with phase("FRESH_PRISTINE"):
            assert_fresh_pristine(request, values)
    with phase("TOOLS"):
        tools = prepare_linux_tools(output / "tools")
    with phase("RUNTIME"):
        runtime = build_runtime(root, request.config.repository, request.source_sha, tools, output / "runtime")
    with phase("IMAGE"):
        image = build_image(runtime, output / "image")
    with phase("SEALER"):
        sealer = build_sealer(root, request.config.repository, request.source_sha, tools, output / "sealer")
    with phase("GENERATION"):
        bodies = build_aggregator(root, request.config.repository, request.source_sha, output / "generation",
                                  seed_secrets={key: values[key] for key in SEED_SECRETS})
    if request.action == "preview":
        with phase("PUBLISH"):
            return preview_suite(request, root, tools, image, sealer, bodies, output / "publish", credentials=values)
    migration = None
    if request.action == "update":
        with phase("MIGRATION"):
            executable = build_cli(root, request.config.repository, request.source_sha, tools, output / "cli")
            migration = prepare_migration(request.config, request.candidate, root, tools, executable, output / "migration")
    deployment = replace(request, action="deploy", previous=None, context=None) if request.action == "repair" else request
    recovery = {"recovery_request": request} if request.action == "repair" and request.context.get("repairId") in (GITHUB_REPAIR_ID, PAGES_REPAIR_ID, ASSETS_REPAIR_ID, GENERATION_REPAIR_ID, PAGES_DIAGNOSTIC_REPAIR_ID, GENERATION_OUTPUTS_REPAIR_ID, PAGES_ASSET_PROTOCOL_REPAIR_ID) else {}
    if fresh_pristine:
        # 长时间本地构建后、首次写入前重核全部空现场；不使用旧 writer 恢复路径。
        with phase("FRESH_PRISTINE"):
            assert_fresh_pristine(request, values)
    with phase("PUBLISH"):
        result = deploy_suite(deployment, root, tools, image, sealer, bodies, output / "publish",
                              credentials=values, migration_settings=migration, **recovery)
    if request.action == "repair":
        require(result == {"checks": {"suite-published": True, "service-ready": True}})
        return {"checks": dict.fromkeys(("repair-completed", "data-preserved", "unchanged-resources-verified", "service-ready"), True)}
    return result
