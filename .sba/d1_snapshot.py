"""MiSub D1 的有界内存快照/副本；源仅 SELECT/PRAGMA 读取，目标单次写入。"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import re

from d1_init import SCHEMA_QUERY, _schema, reviewed_plan


MAX_BYTES = 64 * 1024 * 1024
LITERAL = re.compile(r"(?:NULL|-?[0-9]+(?:\.[0-9]+)?(?:e[+-]?[0-9]+)?|X'[A-F0-9]*'|CAST\(X'[A-F0-9]*' AS TEXT\))\Z")


class SnapshotError(RuntimeError):
    pass


def require(value):
    if not value:
        raise SnapshotError("EASYPROXY_SBA_SNAPSHOT_REJECTED_OR_UNKNOWN")


def identifier(value: str) -> str:
    require(type(value) is str and 0 < len(value.encode("utf-8")) <= 256 and "\x00" not in value)
    return '"' + value.replace('"', '""') + '"'


def encoded_column(name: str) -> str:
    value = identifier(name)
    return (f"CASE typeof({value}) WHEN 'null' THEN 'NULL' WHEN 'integer' THEN CAST({value} AS TEXT) "
        f"WHEN 'real' THEN printf('%!.26g',{value}) WHEN 'blob' THEN 'X''' || hex({value}) || '''' "
        f"WHEN 'text' THEN 'CAST(X''' || hex(CAST({value} AS BLOB)) || ''' AS TEXT)' END")


@dataclass(frozen=True)
class Snapshot:
    schema: tuple[dict, ...]
    tables: dict
    digest: str


def capture(api) -> Snapshot:
    schema = _schema(api.query(SCHEMA_QUERY))
    require(all(row["type"] in {"table", "index", "view", "trigger"} and type(row["sql"]) is str
                and len(row["sql"].encode("utf-8")) <= 65536 for row in schema))
    tables, total = {}, 0
    names = [row["name"] for row in schema if row["type"] == "table"]
    # sqlite_sequence is internal schema but its values are user-data preservation state.
    sequence = api.query("SELECT name FROM sqlite_master WHERE type='table' AND name='sqlite_sequence'")
    require(sequence in ([], [{"name": "sqlite_sequence"}]))
    if sequence:
        names.append("sqlite_sequence")
    for name in names:
        table = identifier(name)
        declaration = next((row["sql"] for row in schema if row["type"] == "table" and row["name"] == name), "")
        require(not re.match(r"\s*CREATE\s+VIRTUAL\s+TABLE", declaration, re.I))
        # MiSub has no foreign keys/generated columns. Refuse an unsupported
        # custom schema BEFORE target writes rather than lossy/unsafe import.
        require(api.query(f"PRAGMA foreign_key_list({table})") == [])
        columns = api.query(f"PRAGMA table_xinfo({table})")
        require(0 < len(columns) <= 256 and all(type(row.get("name")) is str and row.get("hidden") == 0 for row in columns))
        names_only = [row["name"] for row in columns]
        rowid = next((value for value in ("_rowid_", "rowid", "oid") if value not in {value.lower() for value in names_only}), None)
        if name != "sqlite_sequence" and not re.search(r"\bWITHOUT\s+ROWID\b", declaration, re.I):
            require(rowid is not None)
            names_only.insert(0, rowid)
        expressions = [encoded_column(column) + f" AS c{index}" for index, column in enumerate(names_only)]
        order = ",".join(f"c{index}" for index in range(len(names_only)))
        rows, offset = [], 0
        while True:
            page = api.query(f"SELECT {','.join(expressions)} FROM {table} ORDER BY {order} LIMIT 500 OFFSET {offset}")
            require(len(page) <= 500 and all(set(row) == {f"c{index}" for index in range(len(names_only))} for row in page))
            for row in page:
                values = [row[f"c{index}"] for index in range(len(names_only))]
                require(all(type(value) is str and LITERAL.fullmatch(value) is not None for value in values))
                total += sum(len(value) for value in values)
                require(total <= MAX_BYTES and len(rows) < 100000)
                rows.append(values)
            if len(page) < 500:
                break
            offset += 500
        tables[name] = {"columns": names_only, "rows": rows}
    require(_schema(api.query(SCHEMA_QUERY)) == schema)
    body = json.dumps({"schema": schema, "tables": tables}, sort_keys=True, separators=(",", ":")).encode()
    require(len(body) <= MAX_BYTES)
    return Snapshot(tuple(schema), tables, hashlib.sha256(body).hexdigest())


def import_snapshot(owner, permit, source: Snapshot, target):
    require(type(source) is Snapshot and not _schema(target.query(SCHEMA_QUERY)))
    owner.claim_stage(permit, "preview-snapshot-copy")
    for row in source.schema:
        if row["type"] == "table":
            require(re.match(r"\s*CREATE\s+TABLE\b", row["sql"], re.I) is not None)
            owner.assert_owned(permit)
            target.query(row["sql"])
    for name, table in source.tables.items():
        if name == "sqlite_sequence":
            owner.assert_owned(permit)
            target.query("DELETE FROM sqlite_sequence")
        columns = ",".join(identifier(column) for column in table["columns"])
        for row in table["rows"]:
            owner.assert_owned(permit)
            target.query(f"INSERT INTO {identifier(name)} ({columns}) VALUES ({','.join(row)})")
    for row in source.schema:
        if row["type"] != "table":
            require(re.match(r"\s*CREATE\s+(?:UNIQUE\s+)?(?:INDEX|VIEW|TRIGGER)\b", row["sql"], re.I) is not None)
            owner.assert_owned(permit)
            target.query(row["sql"])
    require(capture(target).digest == source.digest)


def rehearse_migration(owner, permit, root, target):
    before = capture(target)
    plan, expected = reviewed_plan(root)
    owner.claim_stage(permit, "preview-migration")
    for statement in plan:
        owner.assert_owned(permit)
        target.query(statement)
    after = capture(target)
    actual_schema = {(row["type"], row["name"]): row for row in after.schema}
    for row in before.schema:
        require(actual_schema.get((row["type"], row["name"])) == row)
    for row in expected:
        require(actual_schema.get((row["type"], row["name"])) == row)
    allowed = {(row["type"], row["name"]) for row in [*before.schema, *expected]}
    require(set(actual_schema) == allowed)
    for name, table in before.tables.items():
        if name != "schema_migrations":
            require(after.tables.get(name) == table)
    require(target.query("SELECT migration_id,name FROM schema_migrations ORDER BY migration_id") ==
            [{"migration_id": 1, "name": "0001_initial"}, {"migration_id": 2, "name": "0002_cron_executions"}])
    return after
