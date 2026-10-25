"""准确迁移在空库上实际执行；保留旧数据库、持久writer和单次阶段语义。"""
import hashlib
from pathlib import Path
import sqlite3
import subprocess
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import d1_init as d1
from coordination import Coordinator, CoordinationError, inventory_digest
from test_coordination import IDENTITY, OLD, MemoryStore, state


ROOT = Path(__file__).resolve().parents[2]
DATABASE = {"kind": "d1", "id": "12345678-1234-1234-1234-123456789abc", "name": "fixture-misub"}


def source(tmp_path):
    root = tmp_path / "root"
    directory = root / "upstreams/misub/migrations"
    directory.mkdir(parents=True)
    for name, digest in d1.MIGRATIONS.items():
        body = subprocess.run(["git", "-C", str(ROOT / "upstreams/misub"), "show",
            "fa684ce1912f5f16ff3b65233ec32103a547215d:migrations/" + name],
            capture_output=True, check=True).stdout
        assert hashlib.sha256(body).hexdigest() == digest
        (directory / name).write_bytes(body)
    return root


class SQLiteAPI:
    def __init__(self):
        self.database = sqlite3.connect(":memory:")
        self.database.row_factory = sqlite3.Row
        self.writes = []
        self.closed = False
        self.identity_value = {"uuid": DATABASE["id"], "name": DATABASE["name"]}
        self.fail = False

    def identity(self):
        return self.identity_value

    def query(self, sql):
        if not sql.startswith("SELECT "):
            self.writes.append(sql)
            self.database.execute(sql)
            if self.fail:
                raise d1.D1Error("EASYPROXY_SBA_D1_REQUEST_UNKNOWN")
            return []
        return [dict(row) for row in self.database.execute(sql)]

    def close(self):
        self.closed = True


def setup(tmp_path, monkeypatch):
    root = source(tmp_path)
    owner = Coordinator(MemoryStore(), IDENTITY, inventory_digest([DATABASE]))
    permit = owner.begin("deploy", OLD)
    api = SQLiteAPI()
    monkeypatch.setattr(d1, "inspect_checkout", lambda *_: [{"path": "upstreams/misub"}])
    monkeypatch.setattr(d1, "inspect_gitlink_checkout", lambda *_: None)

    def client(account, token, database_id):
        assert account == IDENTITY["accountId"] and token == "fixture" and database_id == DATABASE["id"]
        return api

    monkeypatch.setattr(d1, "D1API", client)
    return root, owner, permit, api


def test_approved_initialization_executes_real_sql_and_does_not_commit_suite(tmp_path, monkeypatch):
    root, owner, permit, api = setup(tmp_path, monkeypatch)
    result = d1.initialize_d1(owner, permit, root, [DATABASE], api_token="fixture")
    assert result["checks"] == {"d1-initialized": True, "schema-verified": True}
    assert len(api.writes) == 11 and api.closed
    assert state(owner.store)["writer"] is not None and state(owner.store)["active"] is None
    assert (permit.operation_id, "database-initialization") in owner.store.stages


@pytest.mark.parametrize("sql", ["CREATE TABLE user_data (secret TEXT)",
    "CREATE TABLE subscriptions (id TEXT)", "CREATE TABLE _unrelated (data TEXT)"])
def test_nonempty_unowned_database_is_never_overwritten(tmp_path, monkeypatch, sql):
    root, owner, permit, api = setup(tmp_path, monkeypatch)
    api.database.execute(sql)
    with pytest.raises(d1.D1Error, match="UNOWNED_DATABASE"):
        d1.initialize_d1(owner, permit, root, [DATABASE], api_token="fixture")
    assert not api.writes and not owner.store.stages and api.closed


def test_provider_internal_table_is_preserved_but_not_treated_as_user_schema(tmp_path, monkeypatch):
    root, owner, permit, api = setup(tmp_path, monkeypatch)
    api.database.execute('CREATE TABLE "_cf_KV" (key TEXT PRIMARY KEY, value BLOB)')
    api.database.execute('INSERT INTO "_cf_KV" VALUES (?, ?)', ("provider", b"opaque"))
    d1.initialize_d1(owner, permit, root, [DATABASE], api_token="fixture")
    assert tuple(api.database.execute('SELECT * FROM "_cf_KV"').fetchone()) == ("provider", b"opaque")


@pytest.mark.parametrize("field", ["uuid", "name"])
def test_database_identity_drift_is_rejected_before_schema_write(tmp_path, monkeypatch, field):
    root, owner, permit, api = setup(tmp_path, monkeypatch)
    api.identity_value[field] = "other"
    with pytest.raises(d1.D1Error, match="IDENTITY"):
        d1.initialize_d1(owner, permit, root, [DATABASE], api_token="fixture")
    assert not api.writes and not owner.store.stages


def test_inventory_mismatch_rejected_before_provider(tmp_path, monkeypatch):
    root, owner, permit, api = setup(tmp_path, monkeypatch)
    with pytest.raises(d1.D1Error, match="INVENTORY"):
        d1.initialize_d1(owner, permit, root, [{**DATABASE, "id": "other"}], api_token="fixture")
    assert not api.writes


def test_unknown_write_is_not_retried_and_writer_is_retained(tmp_path, monkeypatch):
    root, owner, permit, api = setup(tmp_path, monkeypatch)
    api.fail = True
    with pytest.raises(d1.D1Error, match="UNKNOWN"):
        d1.initialize_d1(owner, permit, root, [DATABASE], api_token="fixture")
    assert len(api.writes) == 1 and api.closed
    with pytest.raises(d1.D1Error, match="UNOWNED_DATABASE"):
        d1.initialize_d1(owner, permit, root, [DATABASE], api_token="fixture")
    assert len(api.writes) == 1 and state(owner.store)["writer"] is not None


def test_lost_response_before_first_schema_write_cannot_reenter_same_stage(tmp_path, monkeypatch):
    root, owner, permit, api = setup(tmp_path, monkeypatch)
    owner.claim_stage(permit, "database-initialization")
    with pytest.raises(CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        d1.initialize_d1(owner, permit, root, [DATABASE], api_token="fixture")
    assert not api.writes


@pytest.mark.parametrize("mutation", ["changed", "unknown", "symlink"])
def test_only_exact_reviewed_sql_is_accepted(tmp_path, mutation):
    root = source(tmp_path)
    directory = root / "upstreams/misub/migrations"
    if mutation == "changed":
        (directory / "0001_initial.sql").write_text("DROP TABLE subscriptions", encoding="utf-8")
    elif mutation == "unknown":
        (directory / "0003_unknown.sql").write_text("SELECT 1", encoding="utf-8")
    else:
        target = directory / "0001_initial.sql"
        original = tmp_path / "original.sql"
        target.rename(original)
        try:
            target.symlink_to(original)
        except OSError:
            pytest.skip("OS symlink privilege not available")
    with pytest.raises(d1.D1Error, match="SOURCE"):
        d1.reviewed_plan(root)
