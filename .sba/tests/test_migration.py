"""套件许可到真实 S3 传输的阶段测试；CLI 边界在 Go 中另以 SQLite/age 实测。"""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys

import pytest

from test_coordination_s3 import coordination, wire, IDENTITY, RELEASE

sys.modules.setdefault("coordination", coordination)
spec = importlib.util.spec_from_file_location("easyproxy_sba_migration", Path(__file__).parents[1] / "migration.py")
migration = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = migration
spec.loader.exec_module(migration)

NEW = {"sourceSha": "b" * 40, "applicationVersion": "1.1.0"}
DATABASE = "11111111-2222-3333-4444-555555555555"
# This is only an opaque stand-in at the mocked CLI seam. Go integration tests
# create, decrypt, validate and restore genuine age archives using real SQLite.
CIPHERTEXT = b"age-encryption.org/v1\n" + b"opaque-test-ciphertext" * 20


def held(wire):
    create, record = wire
    owner = coordination.Coordinator(create(), IDENTITY, "d" * 64)
    first = owner.begin("deploy", RELEASE)
    owner.commit_verified(first, {"suite-published": True, "service-ready": True})
    return owner, owner.begin("update", NEW, previous=RELEASE), record


def settings(tmp_path):
    manifest = tmp_path / "manifest.json"
    manifest.write_text(json.dumps({"source": {"root_commit": NEW["sourceSha"]},
                                    "resources": [{"kind": "d1", "id": DATABASE}, {"kind": "pages", "id": "easyproxy-testing-pages",
                                                                                 "name": "easyproxy-testing-pages"}]}), encoding="utf-8")
    return migration.MiSubMigration(root=tmp_path, executable=tmp_path / "easyproxyctl.exe", node=tmp_path / "node.exe",
                                   wrangler=tmp_path / "wrangler.js", topology=tmp_path / "topology.yaml", manifest=manifest,
                                   wrangler_config=tmp_path / "wrangler.json", workdir=tmp_path / "operation", database_id=DATABASE,
                                   base_url="https://easyproxy-testing-pages.pages.dev", allow_direct_d1_fallback=True)


def executor(record, outcome="ok"):
    calls = []

    def run(command, **kwargs):
        calls.append(command)
        assert kwargs["env"]["CLOUDFLARE_ACCOUNT_ID"] == IDENTITY["accountId"]
        assert "GITHUB_TOKEN" not in kwargs["env"]
        child_env = kwargs["env"]
        assert child_env["HOME"] == str(Path(child_env["TEMP"]) / "home")
        assert "must-not-reach-child" not in " ".join(child_env.values())
        assert child_env["CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV"] == "false"
        assert Path(child_env["NPM_CONFIG_USERCONFIG"]).read_bytes() == b""
        assert child_env["PATH"].split(os.pathsep)[0] == str(Path(command[command.index("--wrangler-command") + 1]).parent)
        if command[2] == "backup":
            Path(command[command.index("--output") + 1]).write_bytes(CIPHERTEXT)
            assert not record["backups"]
            return "encrypted backup created"
        assert command[2] == "migrate" and "--backup-input" in command and "--backup-output" not in command
        assert command[command.index("--expected-backup-sha256") + 1] == hashlib.sha256(CIPHERTEXT).hexdigest()
        assert list(record["backups"].values()) == [CIPHERTEXT]
        if outcome == "unknown":
            raise migration.MigrationError("EASYPROXY_SBA_MIGRATION_CLI_UNKNOWN")
        evidence = {"schema_version": 1, "database_id": DATABASE, "backup_sha256": hashlib.sha256(CIPHERTEXT).hexdigest(),
                    "before_sha256": "a" * 64, "after_sha256": "b" * 64, "backup_created": True,
                    "data_preserved": True, "migration_verified": True}
        if outcome == "bad-evidence":
            evidence["data_preserved"] = 1
        return json.dumps(evidence)
    return run, calls


def test_phase_retains_before_migration_and_never_commits_suite(wire, tmp_path, monkeypatch):
    owner, permit, record = held(wire)
    for key in ("GITHUB_TOKEN", "HOME", "USERPROFILE", "NODE_OPTIONS", "NODE_PATH", "NPM_CONFIG_USERCONFIG"):
        monkeypatch.setenv(key, "must-not-reach-child")
    run, calls = executor(record)
    result = migration.migrate_misub(owner, permit, settings(tmp_path), run=run)
    assert result["checks"] == {"backup-created": True, "data-preserved": True, "migration-verified": True}
    assert result["backup"]["key"] == f"_sba/backups/{permit.operation_id}/misub.age"
    assert len(calls) == 2
    assert all(command[0].endswith("easyproxyctl.exe") for command in calls)
    assert json.loads(record["body"])["active"] == RELEASE
    assert json.loads(record["body"])["writer"]["operationId"] == permit.operation_id
    assert len(record["writes"]) == 4  # deploy acquire/commit, update acquire, immutable backup
    assert record["writes"][-1]["headers"]["if-none-match"] == "*"
    assert record["writes"][-1]["body"] == CIPHERTEXT
    assert not any(method == "DELETE" for method, _ in record["requests"])
    with pytest.raises(coordination.CoordinationError, match="BUSY"):
        owner.current_release()


@pytest.mark.parametrize("mode", ["server-error", "response-lost", "missing-etag", "backup-corrupt"])
def test_unknown_retention_never_starts_migration_or_unlocks(wire, tmp_path, mode):
    owner, permit, record = held(wire)
    record["mode"] = mode
    run, calls = executor(record)
    with pytest.raises(migration.MigrationError, match="BACKUP_UNKNOWN"):
        migration.migrate_misub(owner, permit, settings(tmp_path), run=run)
    assert len(calls) == 1
    assert len(record["writes"]) == 4
    assert json.loads(record["body"])["writer"]["operationId"] == permit.operation_id


@pytest.mark.parametrize("outcome", ["unknown", "bad-evidence"])
def test_unknown_migration_and_invalid_evidence_keep_backup_and_writer(wire, tmp_path, outcome):
    owner, permit, record = held(wire)
    run, calls = executor(record, outcome)
    with pytest.raises(migration.MigrationError):
        migration.migrate_misub(owner, permit, settings(tmp_path), run=run)
    assert len(calls) == 2 and len(record["writes"]) == 4
    assert list(record["backups"].values()) == [CIPHERTEXT]
    assert json.loads(record["body"])["writer"]["operationId"] == permit.operation_id


def test_backup_retry_handler_cannot_send_second_put(wire, tmp_path):
    owner, permit, record = held(wire)
    owner.store.client.meta.events.register_first("needs-retry.s3.PutObject", lambda attempts, **_kwargs: 0 if attempts < 3 else None)
    record["mode"] = "server-error"
    path = tmp_path / "before.age"
    path.write_bytes(CIPHERTEXT)
    with pytest.raises(migration.MigrationError, match="BACKUP_UNKNOWN"):
        migration.retain_backup(owner, permit, path)
    assert len(record["writes"]) == 4


def test_source_and_backup_format_are_checked_before_remote_backup(wire, tmp_path):
    owner, permit, record = held(wire)
    config = settings(tmp_path)
    config.manifest.write_text('{"source":{"root_commit":"wrong"},"resources":[]}', encoding="utf-8")
    with pytest.raises(migration.MigrationError, match="SOURCE"):
        migration.migrate_misub(owner, permit, config)
    assert len(record["writes"]) == 3
    path = tmp_path / "plaintext.sql"
    path.write_text("sensitive plaintext must not be retained as ciphertext", encoding="utf-8")
    with pytest.raises(migration.MigrationError, match="BACKUP"):
        migration.retain_backup(owner, permit, path)
    assert len(record["writes"]) == 3
