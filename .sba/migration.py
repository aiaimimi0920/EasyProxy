"""真实升级中的 MiSub 保护阶段；持有套件许可，先持久备份，再执行受限迁移。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

from coordination import Coordinator, Permit, S3Store, StoreUnknown


class MigrationError(RuntimeError):
    """只提供固定错误码，不转发 CLI、SQL、订阅或 provider 原文。"""


@dataclass(frozen=True)
class MiSubMigration:
    root: Path
    executable: Path
    node: Path
    wrangler: Path
    topology: Path
    manifest: Path
    wrangler_config: Path
    workdir: Path
    database_id: str
    base_url: str
    allow_direct_d1_fallback: bool = False
    wrangler_dir: Path | None = None


def _require(condition: bool, code: str) -> None:
    if not condition:
        raise MigrationError("EASYPROXY_SBA_MIGRATION_" + code)


def _json(raw: str) -> dict:
    def unique(pairs):
        value = {}
        for key, item in pairs:
            _require(key not in value, "EVIDENCE")
            value[key] = item
        return value
    try:
        _require(len(raw.encode("utf-8")) <= 1024 * 1024, "EVIDENCE")
        value = json.loads(raw, object_pairs_hook=unique)
        _require(type(value) is dict, "EVIDENCE")
        return value
    except (ValueError, UnicodeError, RecursionError):
        raise MigrationError("EASYPROXY_SBA_MIGRATION_EVIDENCE") from None


def _run(command: list[str], *, cwd: Path, env: dict[str, str], timeout: int) -> str:
    try:
        result = subprocess.run(command, cwd=cwd, env=env, capture_output=True,
                                encoding="utf-8", errors="strict", timeout=timeout, check=False)
        _require(result.returncode == 0, "CLI_FAILED")
        _require(len(result.stdout.encode("utf-8")) <= 16384, "EVIDENCE")
        return result.stdout
    except (OSError, UnicodeError, subprocess.SubprocessError):
        raise MigrationError("EASYPROXY_SBA_MIGRATION_CLI_UNKNOWN") from None


def retain_backup(owner: Coordinator, permit: Permit, path: Path) -> dict:
    """只创建本次 operation 的私有新对象；不覆盖、不删除、不重发未知 PUT。"""
    owner.assert_owned(permit)
    _require(permit.action == "update" and isinstance(owner.store, S3Store), "OWNERSHIP")
    _require(path.is_file() and not path.is_symlink() and 0 < path.stat().st_size <= 512 * 1024 * 1024, "BACKUP")
    with path.open("rb") as stream:
        _require(stream.read(22) == b"age-encryption.org/v1\n", "BACKUP")
        stream.seek(0)
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    size = path.stat().st_size
    key = f"_sba/backups/{permit.operation_id}/misub.age"
    store = owner.store
    sent = False

    def once(request, **_kwargs):
        nonlocal sent
        if sent:
            raise StoreUnknown()
        sent = True

    event, handler = "before-send.s3.PutObject", "easyproxy-sba-backup-" + permit.operation_id
    with store._write_mutex:
        store.client.meta.events.register_first(event, once, unique_id=handler)
        try:
            with path.open("rb") as stream:
                result = store.client.put_object(Bucket=store.bucket, Key=key, Body=stream, ContentLength=size,
                                                 ContentType="application/octet-stream", CacheControl="no-store", IfNoneMatch="*")
            _require(isinstance(result.get("ETag"), str) and re.fullmatch(r'"[a-fA-F0-9]{32}"', result["ETag"]) is not None,
                     "BACKUP_UNKNOWN")
        except Exception:
            raise MigrationError("EASYPROXY_SBA_MIGRATION_BACKUP_UNKNOWN") from None
        finally:
            store.client.meta.events.unregister(event, unique_id=handler)
    try:
        response = store.client.get_object(Bucket=store.bucket, Key=key)
        stream = response["Body"]
        remote = hashlib.sha256()
        count = 0
        try:
            while chunk := stream.read(1024 * 1024):
                count += len(chunk)
                _require(count <= size, "BACKUP_UNKNOWN")
                remote.update(chunk)
        finally:
            stream.close()
        _require(count == size and remote.hexdigest() == digest, "BACKUP_UNKNOWN")
    except Exception:
        raise MigrationError("EASYPROXY_SBA_MIGRATION_BACKUP_UNKNOWN") from None
    owner.assert_owned(permit)
    return {"key": key, "sha256": digest, "size": size}


def migrate_misub(owner: Coordinator, permit: Permit, settings: MiSubMigration, *, run=_run,
                  credentials: dict | None = None) -> dict:
    """不声明 suite-published，不提交/释放许可；后续整套发布仍由同一调用方拥有。"""
    state = owner.assert_owned(permit)
    _require(permit.action == "update", "OWNERSHIP")
    for path in (settings.root, settings.executable, settings.node, settings.wrangler, settings.topology,
                 settings.manifest, settings.wrangler_config, settings.workdir):
        _require(isinstance(path, Path) and path.is_absolute() and not str(path).startswith(("\\\\", "//")), "PATH")
    _require(not settings.workdir.exists() and re.fullmatch(r"[a-f0-9-]{36}", settings.database_id) is not None, "IDENTITY")
    if settings.wrangler_dir is not None:
        _require(settings.wrangler_dir.is_absolute() and settings.wrangler_dir.is_dir()
                 and settings.wrangler_dir.resolve(strict=True) == settings.wrangler_dir, "PATH")
    # Prefix is split by the existing Go CLI with strings.Fields. Do not silently
    # reinterpret a tool path containing whitespace or invoke a command shell.
    _require(not any(character.isspace() for character in str(settings.wrangler)), "PATH")
    try:
        manifest = _json(settings.manifest.read_text(encoding="utf-8"))
    except (OSError, UnicodeError):
        raise MigrationError("EASYPROXY_SBA_MIGRATION_SOURCE") from None
    _require(type(manifest.get("source")) is dict and manifest["source"].get("root_commit") ==
             state["writer"]["candidate"]["sourceSha"], "SOURCE")
    _require(type(manifest.get("resources")) is list and all(type(item) is dict for item in manifest["resources"]), "IDENTITY")
    resources = [item for item in manifest["resources"] if item.get("kind") == "d1"]
    _require(len(resources) == 1 and resources[0].get("id") == settings.database_id, "IDENTITY")
    pages = [item for item in manifest["resources"] if item.get("kind") == "pages"]
    _require(len(pages) == 1 and isinstance(pages[0].get("name"), str) and
             re.fullmatch(r"[a-z][a-z0-9-]{1,57}[a-z0-9]", pages[0]["name"]) is not None and
             pages[0].get("id") == pages[0]["name"] and settings.base_url == f"https://{pages[0]['name']}.pages.dev", "IDENTITY")
    settings.workdir.mkdir(parents=True, exist_ok=False)
    names = {"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "SYSTEMDRIVE"}
    secret_names = {"CLOUDFLARE_API_TOKEN", "MISUB_ADMIN_PASSWORD", "EASYPROXY_BACKUP_PASSPHRASE"}
    if credentials is None:
        names |= secret_names  # Backward-compatible stage API; official entry passes explicit values.
    else:
        from sealed_box import secret_value
        _require(type(credentials) is dict and set(credentials) == secret_names
                 and all(secret_value(value) for value in credentials.values()), "CREDENTIALS")
    env = {key: value for key, value in os.environ.items() if key.upper() in names}
    if credentials is not None:
        env.update(credentials)
    home = settings.workdir / "home"
    home.mkdir()
    for name in ("npm-user.rc", "npm-global.rc"):
        (home / name).write_text("", encoding="utf-8")
    env.update(CLOUDFLARE_ACCOUNT_ID=owner.identity["accountId"], TEMP=str(settings.workdir), TMP=str(settings.workdir),
               TMPDIR=str(settings.workdir), RUNNER_TEMP=str(settings.workdir), CI="true", WRANGLER_SEND_METRICS="false",
               HOME=str(home), USERPROFILE=str(home), APPDATA=str(home), LOCALAPPDATA=str(home),
               CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV="false", NPM_CONFIG_USERCONFIG=str(home / "npm-user.rc"),
               NPM_CONFIG_GLOBALCONFIG=str(home / "npm-global.rc"))
    env["PATH"] = str(settings.node.parent) + os.pathsep + env.get("PATH", os.defpath)
    common = ["--topology", str(settings.topology), "--manifest", str(settings.manifest),
              "--wrangler-config", str(settings.wrangler_config), "--wrangler-command", str(settings.node),
              "--wrangler-prefix", str(settings.wrangler),
              "--wrangler-dir", str(settings.wrangler_dir or settings.root / "upstreams/misub")]
    backup = settings.workdir / "misub-before-update.age"
    backup_command = [str(settings.executable), "cloud", "backup", *common, "--output", str(backup), "--base-url", settings.base_url]
    if settings.allow_direct_d1_fallback:
        backup_command.append("--allow-direct-d1-fallback")
    run(backup_command, cwd=settings.root, env=env, timeout=1200)
    retained = retain_backup(owner, permit, backup)
    # Even when backup upload has definitely succeeded, a lost/mismatched read
    # does not authorize migration. Retention is checked before the first D1 write.
    owner.assert_owned(permit)
    result = _json(run([str(settings.executable), "cloud", "migrate", *common,
                       "--backup-input", str(backup), "--expected-backup-sha256", retained["sha256"],
                       "--migrations-dir", str(settings.root / "upstreams/misub/migrations"),
                       "--account-id", owner.identity["accountId"], "--confirm-database-id", settings.database_id],
                      cwd=settings.root, env=env, timeout=1500))
    expected = {"schema_version", "database_id", "backup_sha256", "before_sha256", "after_sha256",
                "backup_created", "data_preserved", "migration_verified"}
    _require(set(result) == expected and type(result["schema_version"]) is int and result["schema_version"] == 1
             and result["database_id"] == settings.database_id and result["backup_sha256"] == retained["sha256"]
             and all(result[key] is True for key in ("backup_created", "data_preserved", "migration_verified"))
             and all(isinstance(result[key], str) and re.fullmatch(r"[a-f0-9]{64}", result[key]) is not None
                     for key in ("before_sha256", "after_sha256")), "EVIDENCE")
    owner.assert_owned(permit)
    return {"checks": {"backup-created": True, "data-preserved": True, "migration-verified": True}, "backup": retained,
            "beforeSha256": result["before_sha256"], "afterSha256": result["after_sha256"]}
