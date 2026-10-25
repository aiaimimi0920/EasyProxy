"""真实 SQLite 副本/隔离加真实 Coordinator；云阶段替身不冒充线上演练。"""
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import deploy
import preview
import preview_isolation
from coordination import Coordinator
from d1_init import reviewed_plan
from d1_snapshot import capture, rehearse_migration
from suite_config import parse_configuration
from test_aggregator_publish import BODIES
from test_coordination import CHECKS, MemoryStore, state
from test_d1_snapshot import SQLite
from test_deploy import prepared
from test_request import fixture, parse


@pytest.fixture
def sandbox(prepared, monkeypatch):
    _, root, tools, image, sealer, output, values, store, record = prepared
    request = parse(fixture("preview"))
    original = parse_configuration(request.context["source"]["configuration"], repository=request.config.repository, environment="production")
    source_store = MemoryStore()
    source_store.bucket = original.bucket
    source_owner = Coordinator(source_store, original.identity("production"), original.inventory_digest)
    permit = source_owner.begin("deploy", request.previous)
    source_owner.commit_verified(permit, CHECKS["deploy"])
    source, target = SQLite(), SQLite()
    for statement in reviewed_plan(Path(__file__).resolve().parents[2])[0]:
        source.query(statement)
    source.query("INSERT INTO subscriptions(id,data) VALUES ('user','{\"secret\":\"preserved\"}')")
    source.query("INSERT INTO settings(key,value) VALUES ('worker_settings_v1','{\"BotToken\":\"source-notification\",\"ChatID\":\"source-chat\",\"aggregatorSync\":{\"enabled\":true},\"webdavBackup\":{\"enabled\":true}}')")
    baseline = capture(source).digest
    source.close = lambda: None
    target.close = lambda: None
    source.identity = lambda: {"uuid": original.database_id, "name": original.database_name}
    target.identity = lambda: {"uuid": request.config.database_id, "name": request.config.database_name}
    source_store.client = store.client
    monkeypatch.setattr(preview, "r2_store", lambda _account, bucket, *_: source_store if bucket == original.bucket else store)
    monkeypatch.setattr(preview, "D1API", lambda _account, _token, database: source if database == original.database_id else target)
    monkeypatch.setattr(preview, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(preview, "reviewed_workflow", lambda *_: b"approved workflow")
    monkeypatch.setattr(preview, "private_record", lambda _store, value: record.update(provenance=value) or value)
    for name in ("PublicArtifacts", "ArtifactStore", "publish_ech", "publish_pages", "publish_candidate",
                 "audit_candidate", "promote_candidate"):
        monkeypatch.setattr(preview, name, getattr(deploy, name))

    def github(owner, permit, *_args, **kwargs):
        assert kwargs["secrets"] == {} and kwargs["sealer"] is None and owner.identity["purpose"] == "preview"
        record["order"].append("github")
        if record["fail"] == "github":
            raise RuntimeError("FIXTURE_UNKNOWN")
        return {"checks": dict.fromkeys(("github-environment-configured", "github-environment-verified", "github-workflow-verified"), True),
                "refreshEnabled": False}

    def migration(owner, permit, _root, api):
        record["order"].append("migration")
        if record["fail"] == "migration":
            raise RuntimeError("FIXTURE_UNKNOWN")
        return rehearse_migration(owner, permit, Path(__file__).resolve().parents[2], api)

    def synchronized(owner, permit, *_args, **_kwargs):
        settings = json.loads(target.query("SELECT value FROM settings WHERE key='worker_settings_v1'")[0]["value"])
        assert not settings["BotToken"] and not settings["ChatID"] and not settings["aggregatorSync"]["enabled"]
        assert settings["aggregatorSync"]["runOnCron"] is False and not settings["webdavBackup"]["enabled"]
        return deploy.sync_misub(owner, permit, *_args, **_kwargs)

    def verify(_config, _release, _root):
        record["order"].append("verify")
        if record["fail"] == "verify":
            raise RuntimeError("FIXTURE_UNKNOWN")
        if record.get("sourceDrift"):
            source.query("INSERT INTO subscriptions(id,data) VALUES ('new','{}')")
        return {"checks": dict.fromkeys(("release-versions-verified", "published-subscriptions-verified", "pages-frontend-verified"), True),
                "runId": record["permit"].operation_id, "manifestSha256": "f" * 64, "counts": {"nodes": 1, "sources": 1}}

    def isolated(_config):
        record["order"].append("isolation")
        if record["fail"] == "isolation":
            raise RuntimeError("FIXTURE_UNKNOWN")

    monkeypatch.setattr(preview, "configure_environment", github)
    monkeypatch.setattr(preview, "rehearse_migration", migration)
    monkeypatch.setattr(preview, "sync_misub", synchronized)
    monkeypatch.setattr(preview, "verify_public", verify)
    monkeypatch.setattr(preview_isolation, "verify_isolation", isolated)
    yield request, root, tools, image, sealer, output, values, store, record, source, target, baseline
    source.connection.close()
    target.connection.close()


def run(values):
    request, root, tools, image, sealer, output, credentials, *_ = values
    return preview.preview_suite(request, root, tools, image, sealer, BODIES, output, credentials=credentials)


def test_preview_copies_real_data_rehearses_migration_isolates_and_proves_source_unchanged(sandbox):
    result = run(sandbox)
    request, _, _, _, _, _, _, store, record, source, target, baseline = sandbox
    assert result["checks"] == CHECKS["preview"] and state(store)["active"] == request.candidate and state(store)["writer"] is None
    assert capture(source).digest == baseline
    assert source.query('SELECT * FROM subscriptions') == target.query('SELECT * FROM subscriptions')
    assert result["lifecycle"]["snapshot"]["id"] == baseline and result["lifecycle"]["snapshot"]["createdAt"] > 0
    assert len(result["lifecycle"]["resources"]) == 7 and all(row["status"] == "created" for row in result["lifecycle"]["resources"])
    assert "source-notification" not in json.dumps(record["provenance"])


@pytest.mark.parametrize("stage", ["migration", "github", "edge", "pages", "candidate", "audit", "promotion", "sync", "verify", "isolation"])
def test_preview_unknown_never_commits_or_changes_source(sandbox, stage):
    sandbox[8]["fail"] = stage
    with pytest.raises(RuntimeError, match="FIXTURE_UNKNOWN"):
        run(sandbox)
    assert state(sandbox[7])["writer"] is not None and state(sandbox[7])["active"] is None
    assert capture(sandbox[9]).digest == sandbox[11]


def test_concurrent_source_change_prevents_a_successful_preview_receipt(sandbox):
    sandbox[8]["sourceDrift"] = True
    with pytest.raises(RuntimeError, match="REFRESH_REJECTED"):
        run(sandbox)
    assert state(sandbox[7])["writer"] is not None and state(sandbox[7])["active"] is None
