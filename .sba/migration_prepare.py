"""为既有受保护 CLI 生成准确实例 topology/manifest；不含秘密、不发布资源。"""
from __future__ import annotations

import json
from pathlib import Path

from migration import MiSubMigration
from pages_build import NPM_VERSION, VERSIONS, _copy_source, _environment, _lock, _plain_file, _run
from refresh_context import require
from sources import inspect_checkout, inspect_gitlink_checkout
from suite_config import SuiteConfig
from toolchain import LinuxTools


def topology(config: SuiteConfig) -> dict:
    return {"schema_version": 1, "deployment_name": config.environment, "release_channel": "stable",
        "components": {"aggregator": False, "misub": True, "ech_worker": False, "local_easyproxy": False},
        "cloudflare": {"account_id_env": "CLOUDFLARE_ACCOUNT_ID", "zone_id_env": "", "use_pages_dev": True,
            "use_workers_dev": False, "resources": {"pages_project": config.pages_name, "d1_database": config.database_name,
                "ech_worker": "", "r2_bucket": "", "r2_public_base_url": ""}},
        "aggregator": {"schedule": "0 */6 * * *"}, "misub": {"default_profile": "aggregator-global"},
        "local": {"install_mode": "native", "access_mode": "local_server", "release_channel": "stable"},
        "secrets": {"cloudflare_api_token": "CLOUDFLARE_API_TOKEN", "cloudflare_dns_token": "",
            "misub_admin_password": "MISUB_ADMIN_PASSWORD", "misub_cookie_secret": "MISUB_COOKIE_SECRET",
            "misub_manifest_token": "MISUB_MANIFEST_TOKEN", "misub_cron_secret": "MISUB_CRON_SECRET",
            "misub_backup_passphrase": "EASYPROXY_BACKUP_PASSPHRASE", "ech_token": "ECH_TOKEN",
            "r2_access_key_id": "R2_ACCESS_KEY_ID", "r2_secret_access_key": "R2_SECRET_ACCESS_KEY"}}


def prepare_migration(config: SuiteConfig, release: dict, root: Path, tools: LinuxTools,
                      executable: Path, output: Path) -> MiSubMigration:
    rows = inspect_checkout(root, config.repository, release["sourceSha"])
    row = next(item for item in rows if item["path"] == "upstreams/misub")
    inspect_gitlink_checkout(root, row)
    require(output.is_absolute() and output.resolve() == output and not output.exists()
            and root != output and root not in output.parents and executable.is_absolute() and executable.is_file())
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    env = _environment(output, tools.node)
    project = output / "misub"
    _copy_source(root / row["path"], project)
    lock = _lock(_plain_file(project / "package-lock.json"))
    (project / "package-lock.json").write_bytes(lock)
    _run([str(tools.node), str(tools.npm_cli), "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--include=dev",
          "--include=optional", "--strict-ssl=true"], project, env, "install", timeout=600)
    require(_plain_file(project / "package-lock.json") == lock)
    for package, version in VERSIONS.items():
        require(json.loads(_plain_file(project / "node_modules" / package / "package.json")).get("version") == version)
    require(_run([str(tools.node), str(tools.npm_cli), "--version"], project, env, "npm") == NPM_VERSION)
    path, manifest, wrangler_config = output / "topology.json", output / "deployment-manifest.json", output / "wrangler.json"
    path.write_text(json.dumps(topology(config), indent=2) + "\n", encoding="utf-8")
    wrangler_config.write_text(json.dumps({"d1_databases": [{"binding": "MISUB_DB", "database_name": config.database_name,
        "database_id": config.database_id}]}) + "\n", encoding="utf-8")
    args = [str(executable), "manifest", "build", "--topology", str(path), "--output", str(manifest),
            "--repo-root", str(root), "--root-commit", release["sourceSha"], "--resource-id", "pages=" + config.pages_name,
            "--resource-id", "d1=" + config.database_id]
    for source in rows:
        args.extend(["--submodule-commit", source["path"] + "=" + source["sourceSha"]])
    _run(args, output, env, "manifest")
    value = json.loads(_plain_file(manifest))
    require(value.get("source", {}).get("root_commit") == release["sourceSha"])
    inspect_checkout(root, config.repository, release["sourceSha"])
    return MiSubMigration(root=root, executable=executable, node=tools.node,
        wrangler=project / "node_modules/wrangler/bin/wrangler.js", topology=path, manifest=manifest,
        wrangler_config=wrangler_config, workdir=output / "operation", database_id=config.database_id,
        base_url=f"https://{config.pages_name}.pages.dev", allow_direct_d1_fallback=True, wrangler_dir=project)
