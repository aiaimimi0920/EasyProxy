"""真实 SQLite 数据副本/迁移证明；不是 Cloudflare D1 HTTP 或云端验收。"""
from pathlib import Path
import sqlite3
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from coordination import Coordinator, CoordinationError
from d1_init import reviewed_plan
from d1_snapshot import SnapshotError, capture, import_snapshot, rehearse_migration
from test_coordination import IDENTITY, MemoryStore, NEW, state


class SQLite:
    def __init__(self):
        self.connection = sqlite3.connect(":memory:")
        self.connection.row_factory = sqlite3.Row
        self.calls = []

    def query(self, sql):
        self.calls.append(sql)
        cursor = self.connection.execute(sql)
        return [dict(row) for row in cursor] if cursor.description is not None else []


@pytest.fixture
def copied():
    source, target = SQLite(), SQLite()
    store = MemoryStore()
    owner = Coordinator(store, {**IDENTITY, "purpose": "preview"}, "d" * 64)
    permit = owner.begin("preview", NEW)
    yield source, target, owner, permit, store
    source.connection.close()
    target.connection.close()


def test_actual_copy_preserves_unicode_nul_blobs_real_integers_rowids_and_sequences(copied):
    source, target, owner, permit, store = copied
    source.connection.execute('CREATE TABLE custom (id INTEGER PRIMARY KEY AUTOINCREMENT, text_value TEXT, bytes_value BLOB, number REAL)')
    source.connection.execute('INSERT INTO custom VALUES (?,?,?,?)', (53, "中文\x00末尾'\n", b"\x00\xff\x10", 1.2345678901234567))
    source.connection.execute('INSERT INTO custom VALUES (?,?,?,?)', (90, None, b'', -1e-100))
    source.connection.execute('DELETE FROM custom WHERE id=90')
    source.query('CREATE TABLE implicit (value TEXT)')
    source.query("INSERT INTO implicit (_rowid_,value) VALUES (42,'same')")
    source.query('CREATE INDEX custom_text ON custom(text_value)')
    source.query('CREATE VIEW custom_view AS SELECT id FROM custom')
    source.calls.clear()
    before = capture(source)
    original_queries = len(source.calls)
    import_snapshot(owner, permit, before, target)
    assert capture(target).digest == before.digest == capture(source).digest
    assert source.query('SELECT * FROM sqlite_sequence') == target.query('SELECT * FROM sqlite_sequence')
    assert source.query('SELECT rowid,* FROM implicit') == target.query('SELECT rowid,* FROM implicit')
    assert source.connection.execute('SELECT * FROM custom').fetchone()[:] == target.connection.execute('SELECT * FROM custom').fetchone()[:]
    assert all(sql.startswith(('SELECT ', 'PRAGMA ')) for sql in source.calls[:original_queries])
    assert state(store)["writer"] is not None and state(store)["active"] is None
    with pytest.raises(CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        # Fresh empty target must not permit the same durable stage again.
        extra = SQLite()
        try:
            import_snapshot(owner, permit, before, extra)
        finally:
            extra.connection.close()


def test_approved_migration_preserves_custom_data_and_reaches_exact_schema(copied):
    source, target, owner, permit, _ = copied
    source.query('CREATE TABLE custom (id TEXT PRIMARY KEY,value TEXT)')
    source.query("INSERT INTO custom VALUES ('data','preserved')")
    snapshot = capture(source)
    import_snapshot(owner, permit, snapshot, target)
    root = Path(__file__).resolve().parents[2]
    after = rehearse_migration(owner, permit, root, target)
    assert after.tables["custom"] == snapshot.tables["custom"]
    assert capture(source).digest == snapshot.digest
    assert target.query('SELECT COUNT(*) AS count FROM subscriptions') == [{"count": 0}]


def test_existing_misub_data_is_not_reinitialized_during_preview(copied):
    source, target, owner, permit, _ = copied
    root = Path(__file__).resolve().parents[2]
    plan, _ = reviewed_plan(root)
    for statement in plan:
        source.query(statement)
    source.query("INSERT INTO subscriptions(id,data) VALUES ('user','{\"private\":true}')")
    snapshot = capture(source)
    import_snapshot(owner, permit, snapshot, target)
    after = rehearse_migration(owner, permit, root, target)
    assert after.tables["subscriptions"] == snapshot.tables["subscriptions"]
    assert source.query('SELECT * FROM subscriptions') == target.query('SELECT * FROM subscriptions')


@pytest.mark.parametrize("ddl", ['CREATE TABLE child (id INTEGER REFERENCES parent(id))',
    'CREATE TABLE generated (value INTEGER,calculated INTEGER GENERATED ALWAYS AS (value+1))'])
def test_unsupported_custom_schema_fails_before_any_copy_write(copied, ddl):
    source, target, _, _, _ = copied
    source.query('CREATE TABLE parent (id INTEGER PRIMARY KEY)')
    source.query(ddl)
    with pytest.raises(SnapshotError):
        capture(source)
    assert not target.calls


def test_nonempty_target_is_not_imported_or_replaced(copied):
    source, target, owner, permit, _ = copied
    target.query('CREATE TABLE user_data(value TEXT)')
    before = len(target.calls)
    with pytest.raises(SnapshotError):
        import_snapshot(owner, permit, capture(source), target)
    assert len(target.calls) == before + 1
    assert not owner.store.stages
