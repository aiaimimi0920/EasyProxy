"""周期刷新使用真实共同状态机；阶段替身不冒充线上业务验收。"""
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import deploy
import refresh
import refresh_select
from coordination import Coordinator, CoordinationError
from refresh_context import CONFIG_VARIABLE, OWNER_VARIABLE, REFRESH_SECRETS, RefreshError, context, manifest_release
from refresh_registry import RegistryError, include_environment, parse_registry
from suite_config import WORKFLOW
from test_coordination import CHECKS, state
from test_deploy import prepared
from test_aggregator_publish import BODIES


ORDER = ["candidate", "audit", "promotion", "sync", "verify"]
NEW = {"sourceSha": "c" * 40, "applicationVersion": "2.0.0"}


def environment(config, *, purpose="production"):
    marker = {"schemaVersion": 1, "repositoryId": 1, "environmentId": 2,
              "identity": config.identity(purpose), "inventoryDigest": config.inventory_digest}
    return {"GITHUB_REPOSITORY": config.repository, "EASYPROXY_SBA_ENVIRONMENT": config.environment,
            OWNER_VARIABLE: json.dumps(marker), CONFIG_VARIABLE: json.dumps(config.configuration()),
            **dict.fromkeys(REFRESH_SECRETS, "fixture-value")}


@pytest.fixture
def refreshed(prepared, monkeypatch):
    request, root, _, image, _, output, values, store, record = prepared
    owner = Coordinator(store, request.config.identity("production"), request.config.inventory_digest)
    initial = owner.begin("deploy", request.candidate)
    owner.commit_verified(initial, CHECKS["deploy"])
    for name in ("r2_store", "PublicArtifacts", "ArtifactStore", "inspect_checkout", "publish_candidate",
                 "audit_candidate", "promote_candidate", "sync_misub"):
        monkeypatch.setattr(refresh, name, getattr(deploy, name))

    def verify(config, release, source):
        assert config == request.config and release == request.candidate and source == root
        assert state(store)["active"] == release and state(store)["writer"] is not None
        record["order"].append("verify")
        if record["fail"] == "verify":
            raise RuntimeError("FIXTURE_UNKNOWN")
        result = {"checks": dict.fromkeys(("release-versions-verified", "published-subscriptions-verified",
                                          "pages-frontend-verified"), True),
                  "runId": record["permit"].operation_id, "manifestSha256": "f" * 64, "counts": {"nodes": 1, "sources": 1}}
        if record["bad"] == "verify":
            result["checks"]["pages-frontend-verified"] = False
        if record.get("manifestDrift"):
            result["manifestSha256"] = "e" * 64
        return result

    monkeypatch.setattr(refresh, "verify_public", verify)
    return request, root, image, output, {name: values[name] for name in REFRESH_SECRETS}, store, record, owner


def run(values):
    request, root, image, output, credentials, *_ = values
    return refresh.refresh_suite(request.config, request.candidate, root, image, BODIES, output, credentials=credentials)


def test_refresh_commits_only_after_consumption_sync_and_exact_public_readback(refreshed):
    assert run(refreshed) == {"checks": CHECKS["refresh"]}
    request, *_, store, record, _ = refreshed
    assert record["order"] == ORDER
    assert state(store)["active"] == request.candidate and state(store)["writer"] is None
    assert len(store.writes) == 4 and record["storeClosed"] and record["publicClosed"]


@pytest.mark.parametrize("stage", ORDER)
def test_refresh_unknown_keeps_original_version_and_writer_and_never_replays(refreshed, stage):
    request, _, _, _, _, store, record, _ = refreshed
    record["fail"] = stage
    with pytest.raises(RuntimeError, match="FIXTURE_UNKNOWN"):
        run(refreshed)
    assert record["order"] == ORDER[:ORDER.index(stage) + 1]
    assert state(store)["active"] == request.candidate and state(store)["writer"] is not None
    assert len(store.writes) == 3 and record["storeClosed"] and record["publicClosed"]


def test_update_between_selection_and_refresh_rejects_before_candidate_write(refreshed):
    request, _, _, _, _, store, record, owner = refreshed
    permit = owner.begin("update", NEW, previous=request.candidate)
    owner.commit_verified(permit, CHECKS["update"])
    with pytest.raises(RefreshError):
        run(refreshed)
    assert not record["order"] and state(store)["active"] == NEW and state(store)["writer"] is None


def test_busy_upgrade_is_not_automatically_stolen(refreshed):
    request, _, _, _, _, store, record, owner = refreshed
    owner.begin("update", NEW, previous=request.candidate)
    with pytest.raises(CoordinationError, match="BUSY"):
        run(refreshed)
    assert not record["order"] and state(store)["writer"]["action"] == "update"


def test_final_readback_cannot_use_another_refresh_manifest(refreshed):
    refreshed[-2]["manifestDrift"] = True
    with pytest.raises(RefreshError):
        run(refreshed)
    assert state(refreshed[-3])["writer"] is not None


def test_extra_admin_credentials_are_rejected_before_creating_cloud_clients(refreshed):
    refreshed[4]["EASYPROXY_GITHUB_TOKEN"] = "must-not-be-present"
    with pytest.raises(RefreshError):
        run(refreshed)
    assert refreshed[-2]["clients"] == 0


def test_preview_marker_rejects_before_reading_repo_or_org_inherited_secrets(refreshed):
    config = refreshed[0].config
    values = environment(config, purpose="preview")

    class NoSecretReads(dict):
        def get(self, name, *args):
            assert name not in REFRESH_SECRETS
            return super().get(name, *args)

    with pytest.raises(RefreshError):
        refresh_select.select_source(refreshed[1], refreshed[3], environment=NoSecretReads(values))
    assert refreshed[-2]["clients"] == 0


@pytest.mark.parametrize("mutation", ["inventory", "repository", "environment", "marker"])
def test_context_identity_drift_fails_without_a_provider_client(refreshed, mutation):
    values = environment(refreshed[0].config)
    if mutation == "marker":
        values[OWNER_VARIABLE] = '{}'
    elif mutation == "inventory":
        marker = json.loads(values[OWNER_VARIABLE])
        marker["inventoryDigest"] = "0" * 64
        values[OWNER_VARIABLE] = json.dumps(marker)
    else:
        values["GITHUB_REPOSITORY" if mutation == "repository" else "EASYPROXY_SBA_ENVIRONMENT"] = "foreign/repo" if mutation == "repository" else "foreign"
    with pytest.raises(RuntimeError):
        context(values)


def test_selector_prepares_r2_committed_source_not_default_branch_or_env_sha(refreshed, monkeypatch):
    request, root, _, output, _, store, record, _ = refreshed
    (root / WORKFLOW).parent.mkdir(parents=True)
    (root / WORKFLOW).write_bytes(b"approved workflow")
    monkeypatch.setattr(refresh_select, "r2_store", lambda *_: store)
    calls = []

    def materialize(target, repository, sha, cache_root):
        calls.append((repository, sha, cache_root))
        (target / WORKFLOW).parent.mkdir(parents=True)
        (target / WORKFLOW).write_bytes(b"approved workflow")

    monkeypatch.setattr(refresh_select, "materialize_workspace", materialize)
    monkeypatch.setattr(refresh_select, "manifest_release", lambda *_: request.candidate)
    values = environment(request.config)
    values.update(GITHUB_SHA="f" * 40, EASYPROXY_APPROVED_SHA="e" * 40)
    selected = refresh_select.select_source(root, output, environment=values)
    assert selected["release"] == request.candidate
    assert calls == [(request.config.repository, request.source_sha, root)]
    assert len(store.writes) == 2 and state(store)["writer"] is None and record["storeClosed"]


def test_manifest_version_is_from_prepared_approved_source(tmp_path):
    (tmp_path / ".sba").mkdir()
    path = tmp_path / ".sba/manifest.json"
    path.write_text(json.dumps({"schemaVersion": 3, "id": "easyproxy-cloud", "version": "1.2.3"}), encoding="utf-8")
    assert manifest_release(tmp_path, "a" * 40) == {"sourceSha": "a" * 40, "applicationVersion": "1.2.3"}
    path.write_text(json.dumps({"schemaVersion": 3, "id": "foreign", "version": "1.2.3"}), encoding="utf-8")
    with pytest.raises(RefreshError):
        manifest_release(tmp_path, "a" * 40)


@pytest.mark.parametrize("raw", ['{}', '[]', '{"schemaVersion":1,"schemaVersion":1}',
    '{"schemaVersion":1,"repository":"fixture/easyproxy","environments":["test","test"]}',
    '{"schemaVersion":1,"repository":"fixture/easyproxy","environments":["bad/name"]}'])
def test_registry_rejects_foreign_shapes_duplicates_or_arbitrary_environment_paths(raw):
    with pytest.raises(RegistryError):
        parse_registry(raw, "fixture/easyproxy")


def test_registry_merges_without_versions_or_secret_values():
    value = parse_registry(None, "fixture/easyproxy")
    value = parse_registry(include_environment(value, "other"), "fixture/easyproxy")
    result = parse_registry(include_environment(value, "testing"), "fixture/easyproxy")
    assert result["environments"] == ["other", "testing"] and set(result) == {"schemaVersion", "repository", "environments"}
