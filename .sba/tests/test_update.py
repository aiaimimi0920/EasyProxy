"""同实例升级保留真实共同 writer；迁移完成前不得发布，阶段失败不得提交。"""
from dataclasses import replace
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import deploy
from coordination import Coordinator
from migration import MiSubMigration
from test_aggregator_publish import BODIES
from test_coordination import CHECKS, OLD, state
from test_deploy import prepared


ORDER = ["migration", "github", "edge", "pages", "candidate", "audit", "promotion", "sync", "verify"]


@pytest.fixture
def upgraded(prepared, monkeypatch):
    request, root, tools, image, sealer, output, credentials, store, record = prepared
    request = replace(request, action="update", previous=OLD)
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    permit = owner.begin("deploy", OLD)
    owner.commit_verified(permit, CHECKS["deploy"])
    settings = MiSubMigration(root, root / "cli", tools.node, root / "wrangler", root / "topology", root / "manifest",
                             root / "wrangler-config", output / "migration", request.config.database_id, request.config.pages_origin)

    def migrate(owner, permit, actual, *, credentials):
        assert actual == settings and set(credentials) == {"CLOUDFLARE_API_TOKEN", "MISUB_ADMIN_PASSWORD", "EASYPROXY_BACKUP_PASSPHRASE"}
        assert all(value == "fixture-value" for value in credentials.values())
        owner.assert_owned(permit)
        record["order"].append("migration")
        if record["fail"] == "migration":
            raise RuntimeError("FIXTURE_UNKNOWN")
        return {"checks": {"backup-created": True, "data-preserved": record["bad"] != "migration", "migration-verified": True}}

    def verify(config, release, source):
        assert state(store)["active"] == OLD and state(store)["writer"]["candidate"] == request.candidate
        assert config == request.config and release == request.candidate and source == root
        record["order"].append("verify")
        if record["fail"] == "verify":
            raise RuntimeError("FIXTURE_UNKNOWN")
        return {"checks": dict.fromkeys(("release-versions-verified", "published-subscriptions-verified", "pages-frontend-verified"), True),
                "runId": record["permit"].operation_id, "manifestSha256": "f" * 64, "counts": {"nodes": 1, "sources": 1}}

    monkeypatch.setattr(deploy, "migrate_misub", migrate)
    monkeypatch.setattr(deploy, "verify_public", verify)
    return request, root, tools, image, sealer, output, credentials, store, record, settings


def run(values):
    request, root, tools, image, sealer, output, credentials, _, _, settings = values
    return deploy.deploy_suite(request, root, tools, image, sealer, BODIES, output,
                               credentials=credentials, migration_settings=settings)


def test_update_requires_backup_and_preservation_before_publishing_and_committing(upgraded):
    assert run(upgraded) == {"checks": CHECKS["update"]}
    assert upgraded[-2]["order"] == ORDER
    assert state(upgraded[-3])["active"] == upgraded[0].candidate and state(upgraded[-3])["writer"] is None
    assert len(upgraded[-3].writes) == 4


@pytest.mark.parametrize("stage", ORDER)
def test_update_partial_or_unknown_never_releases_writer_or_runs_later_stage(upgraded, stage):
    upgraded[-2]["fail"] = stage
    with pytest.raises(RuntimeError, match="FIXTURE_UNKNOWN"):
        run(upgraded)
    assert upgraded[-2]["order"] == ORDER[:ORDER.index(stage) + 1]
    assert state(upgraded[-3])["active"] == OLD and state(upgraded[-3])["writer"] is not None
    assert upgraded[-2]["storeClosed"] and upgraded[-2]["publicClosed"]


def test_invalid_preservation_check_stops_before_any_application_publication(upgraded):
    upgraded[-2]["bad"] = "migration"
    with pytest.raises(deploy.DeploymentError):
        run(upgraded)
    assert upgraded[-2]["order"] == ["migration"]
    assert state(upgraded[-3])["active"] == OLD and state(upgraded[-3])["writer"] is not None


def test_update_cannot_use_another_database_migration_settings(upgraded):
    values = list(upgraded)
    values[-1] = replace(values[-1], database_id="f" * 36)
    with pytest.raises(deploy.DeploymentError):
        run(values)
    assert upgraded[-2]["clients"] == 0 and len(upgraded[-3].writes) == 2
