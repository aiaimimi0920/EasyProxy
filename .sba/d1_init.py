"""首次套件部署的空 D1 初始化；仅批准迁移，单次写入，不隐式导入/重放。"""
from __future__ import annotations

import hashlib
from pathlib import Path
import re
import sqlite3

from cloudflare_http import api_result, secure_session
from coordination import Coordinator, Permit, inventory_digest
from sources import inspect_checkout, inspect_gitlink_checkout
from worker_api import credential


MIGRATIONS = {
    "0001_initial.sql": "50a4e7c1c5246dd52ee94c3130ef46450229c7b8b86505cf40152a0a90a9076f",
    "0002_cron_executions.sql": "cbc96d535f18200955567b7ec625df6a656f7c24d617e954445942c0fb7362ff",
}
SCHEMA_QUERY = "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name"
HISTORY_QUERY = "SELECT migration_id, name FROM schema_migrations ORDER BY migration_id"
COUNT_QUERY = ("SELECT (SELECT COUNT(*) FROM subscriptions) AS subscriptions, "
               "(SELECT COUNT(*) FROM profiles) AS profiles, (SELECT COUNT(*) FROM settings) AS settings, "
               "(SELECT COUNT(*) FROM cron_executions) AS cron_executions")


class D1Error(RuntimeError):
    pass


def require(value, code):
    if not value:
        raise D1Error("EASYPROXY_SBA_D1_" + code)


class D1API:
    def __init__(self, account_id: str, token: str, database_id: str):
        require(isinstance(account_id, str) and re.fullmatch(r"[a-f0-9]{32}", account_id) is not None
                and credential(token) and isinstance(database_id, str)
                and re.fullmatch(r"[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}", database_id) is not None, "IDENTITY")
        self.session, self.token = secure_session(), token
        self.path = f"/accounts/{account_id}/d1/database/{database_id}"

    def close(self):
        self.session.close()

    def identity(self):
        value = api_result(self.session, self.token, "GET", self.path, require)
        require(type(value) is dict, "IDENTITY")
        return value

    def query(self, sql: str) -> list[dict]:
        value = api_result(self.session, self.token, "POST", self.path + "/query", require, json={"sql": sql})
        require(type(value) is list and len(value) == 1 and type(value[0]) is dict
                and value[0].get("success") is True and type(value[0].get("results")) is list
                and all(type(row) is dict for row in value[0]["results"]), "QUERY_UNKNOWN")
        return value[0]["results"]


def _schema(rows):
    require(type(rows) is list and len(rows) <= 1000 and all(type(row) is dict and set(row) ==
            {"type", "name", "tbl_name", "sql"} and isinstance(row["name"], str) for row in rows), "SCHEMA")
    # Ignore only SQLite's own namespace and the provider's exact internal table,
    # not arbitrary user tables which happen to start with an underscore.
    return [row for row in rows if not row["name"].startswith("sqlite_") and row["name"] != "_cf_KV"]


def reviewed_plan(root: Path) -> tuple[list[str], list[dict]]:
    directory = root / "upstreams/misub/migrations"
    require(directory.is_dir() and not directory.is_symlink()
            and {path.name for path in directory.iterdir()} <= {*MIGRATIONS, "README.md"}, "SOURCE")
    statements = []
    for name, digest in MIGRATIONS.items():
        path = directory / name
        require(path.is_file() and not path.is_symlink() and path.resolve(strict=True) == path, "SOURCE")
        body = path.read_bytes()
        require(hashlib.sha256(body).hexdigest() == digest, "SOURCE")
        # Safe only after the full reviewed bytes match; never split arbitrary SQL.
        statements.extend(statement.strip() for statement in body.decode("utf-8").split(";") if statement.strip())
    with sqlite3.connect(":memory:") as database:
        database.row_factory = sqlite3.Row
        for statement in statements:
            database.execute(statement)
        schema = _schema([dict(row) for row in database.execute(SCHEMA_QUERY)])
    return statements, schema


def initialize_d1(owner: Coordinator, permit: Permit, root: Path, inventory: list[dict], *, api_token: str) -> dict:
    require(isinstance(root, Path) and root.is_absolute(), "SOURCE")
    root = root.resolve(strict=True)
    state = owner.assert_owned(permit)
    require(permit.action in ("deploy", "preview") and state["active"] is None, "OWNERSHIP")
    require(inventory_digest(inventory) == owner.inventory_digest, "INVENTORY")
    rows = [row for row in inventory if row["kind"] == "d1"]
    require(len(rows) == 1, "IDENTITY")
    expected = rows[0]
    sha = state["writer"]["candidate"]["sourceSha"]
    links = inspect_checkout(root, owner.identity["repository"], sha)
    inspect_gitlink_checkout(root, next(row for row in links if row["path"] == "upstreams/misub"))
    plan, schema = reviewed_plan(root)
    api = D1API(owner.identity["accountId"], api_token, expected["id"])
    try:
        identity = api.identity()
        require(identity.get("uuid") == expected["id"] and identity.get("name") == expected["name"], "IDENTITY")
        require(not _schema(api.query(SCHEMA_QUERY)), "UNOWNED_DATABASE")
        owner.claim_stage(permit, "database-initialization")
        for statement in plan:
            owner.assert_owned(permit)
            api.query(statement)  # No retry, even though reviewed DDL uses IF NOT EXISTS.
        require(_schema(api.query(SCHEMA_QUERY)) == schema, "SCHEMA")
        require(api.query(HISTORY_QUERY) == [{"migration_id": 1, "name": "0001_initial"},
                                            {"migration_id": 2, "name": "0002_cron_executions"}], "HISTORY")
        require(api.query(COUNT_QUERY) == [{"subscriptions": 0, "profiles": 0, "settings": 0, "cron_executions": 0}], "DATA")
        identity = api.identity()
        require(identity.get("uuid") == expected["id"] and identity.get("name") == expected["name"], "IDENTITY")
        inspect_checkout(root, owner.identity["repository"], sha)
        owner.assert_owned(permit)
        return {"checks": {"d1-initialized": True, "schema-verified": True}, "databaseId": expected["id"]}
    finally:
        api.close()  # The caller still owns writer; initialization is not suite success.
