"""候选/稳定发布必须共享套件许可；故障不可自动重放、回滚或解锁。"""
import base64
import copy
import hashlib
import json
from dataclasses import replace
import importlib
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))
publish = importlib.import_module("aggregator_publish")
from artifact_store import ArtifactError, ArtifactStore, Object
from aggregator_audit import Audit, candidate_digest
from audit_image import AuditImage, image_identity
from coordination import Coordinator, CoordinationError
from coordination import _encode
from test_coordination import CHECKS, IDENTITY, INVENTORY, MemoryStore, OLD, NEW, state


URI = "trojan://fixture@192.0.2.1:443#fixture"
BODIES = {"subs/clash.yaml": b'proxies:\n  - {name: fixture, type: trojan, server: 192.0.2.1, port: 443, password: fixture}\n',
          "subs/singbox.json": b'{"outbounds":[{"type":"trojan","server":"192.0.2.1"}]}',
          "subs/mixed.txt": base64.b64encode((URI + "\n").encode()),
          "subs/v2ray.txt": base64.b64encode((URI + "\n").encode()),
          "internal/crawledsubs.json": b'{"https://source.invalid/sub":{}}'}


@pytest.fixture(autouse=True)
def approved_source(monkeypatch):
    monkeypatch.setattr(publish, "inspect_checkout", lambda root, repository, sha: [
        {"path": "upstreams/aggregator", "sourceSha": "e" * 40}])


class Objects:
    def __init__(self, control):
        self.store = control
        self.objects, self.writes, self.failed_key = {}, [], None

    def get(self, key):
        return self.objects.get(key)

    def put(self, key, body, previous, *, immutable=False):
        assert self.objects.get(key) == previous
        assert not immutable or previous is None
        self.writes.append(key)
        self.objects[key] = Object(body, '"' + hashlib.md5(body).hexdigest() + '"')
        if key == self.failed_key:
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_WRITE_UNKNOWN")
        return self.objects[key]


class Public:
    def __init__(self, objects):
        self.objects, self.seen = objects, []
        self.failed_key = None
        self.origin = "https://fixture.account.workers.dev/aggregator"

    def verify(self, key, body):
        self.seen.append(key)
        assert self.objects.get(key).body == body
        if key == self.failed_key:
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_PUBLIC_UNKNOWN")


def setup():
    control = MemoryStore()
    owner = Coordinator(control, IDENTITY, INVENTORY)
    objects = Objects(control)
    return owner, objects, Public(objects)


def stage(owner, objects, public, permit, bodies=None, uris=None):
    candidate = publish.publish_candidate(owner, permit, objects, public, bodies or copy.deepcopy(BODIES))
    result = publish.promote_candidate(owner, permit, objects, public, candidate,
                                      audit=audit(permit, public, uris), root=Path("fixture"))
    return candidate, result


def audit(permit, public, uris=None):
    image = AuditImage("sha256:" + "f" * 64, IDENTITY["repository"],
                       json.loads(permit.state_bytes)["writer"]["candidate"]["sourceSha"], "9" * 64)
    identity = image_identity(image)
    value = {"audit_id": "sba-" + permit.operation_id, "compat_probe": {"ok": True},
            "container_cleanup_confirmed": True,
            "validated_image": image.image_id, "image_identity": {
                key: identity[key] for key in ("imageId", "sourceSha", "platform", "scope")},
            "inputs": {"subscriptions": [public.origin + f"/candidate/{permit.operation_id}/subs/clash.yaml"],
                       "proxy_uris": [], "manifest_url": ""}, "nodes": {"stable_available_uris": uris or [URI]}}
    return Audit(permit.operation_id, candidate_digest(publish.Candidate(permit.operation_id, BODIES)), "e" * 40, _encode(value), image)


def deployed():
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    candidate, result = stage(owner, objects, public, permit)
    owner.commit_verified(permit, CHECKS["deploy"])
    return owner, objects, public, candidate, result


def test_real_candidate_stable_manifest_hashes_and_writer_stays_held():
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    candidate, result = stage(owner, objects, public, permit)
    assert set(candidate.artifacts) == publish.RAW
    assert result["checks"] == {"candidate-verified": True, "stable-verified": True}
    assert result["counts"] == {"nodes": 1, "sources": 1}
    assert objects.writes[-1] == "manifests/stable.json"
    assert len(objects.writes) == 5 + 7 + 1 + 7 + 1
    assert state(owner.store)["writer"] is not None
    assert state(owner.store)["active"] is None
    assert all(not key.startswith("_sba/") for key in objects.writes)


def test_refresh_and_update_retain_exact_previous_data_and_do_not_commit_suite():
    for action in ("refresh", "update"):
        owner, objects, public, old, _ = deployed()
        previous = objects.get("manifests/stable.json").body
        permit = owner.begin(action, OLD if action == "refresh" else NEW, previous=OLD)
        _, result = stage(owner, objects, public, permit)
        assert objects.get("last-known-good/manifest.json").body == previous
        assert objects.get(f"releases/{old.operation_id}/subs/effective.txt").body == (URI + "\n").encode()
        assert result["runId"] == permit.operation_id
        assert state(owner.store)["writer"]["action"] == action


@pytest.mark.parametrize("key", ["subs/clash.yaml", "manifests/stable.json"])
def test_unknown_after_stable_write_keeps_marker_writer_and_partial_state_without_rollback(key):
    owner, objects, public, _, _ = deployed()
    permit = owner.begin("refresh", OLD, previous=OLD)
    candidate = publish.publish_candidate(owner, permit, objects, public, BODIES)
    objects.failed_key = key
    before = len(objects.writes)
    with pytest.raises(ArtifactError, match="WRITE_UNKNOWN"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=audit(permit, public), root=Path("fixture"))
    after = len(objects.writes)
    assert after > before and objects.writes[-1] == key
    with pytest.raises(CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=audit(permit, public), root=Path("fixture"))
    assert len(objects.writes) == after
    assert state(owner.store)["writer"] is not None


def test_candidate_unknown_and_same_permit_reentry_are_not_retried():
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    objects.failed_key = f"candidate/{permit.operation_id}/subs/clash.yaml"
    with pytest.raises(ArtifactError, match="WRITE_UNKNOWN"):
        publish.publish_candidate(owner, permit, objects, public, BODIES)
    assert len(objects.writes) == 1
    with pytest.raises(CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        publish.publish_candidate(owner, permit, objects, public, BODIES)
    assert len(objects.writes) == 1


def test_candidate_mapping_replacement_is_detected_before_promotion_writes():
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    candidate = publish.publish_candidate(owner, permit, objects, public, BODIES)
    candidate.artifacts["subs/v2ray.txt"] = base64.b64encode(b"ss://different\n")
    before = len(objects.writes)
    with pytest.raises(ArtifactError, match="AUDIT"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=audit(permit, public), root=Path("fixture"))
    assert len(objects.writes) == before


def test_prior_stable_drift_stops_without_repair_or_any_promotion_write():
    owner, objects, public, _, _ = deployed()
    objects.objects["subs/effective.txt"] = Object(b"drift", '"' + "0" * 32 + '"')
    permit = owner.begin("refresh", OLD, previous=OLD)
    candidate = publish.publish_candidate(owner, permit, objects, public, BODIES)
    before = len(objects.writes)
    with pytest.raises(ArtifactError, match="STABLE_DRIFT"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=audit(permit, public), root=Path("fixture"))
    assert len(objects.writes) == before
    assert objects.objects["subs/effective.txt"].body == b"drift"


@pytest.mark.parametrize("mutation", [lambda b: b.update({"_sba/control-v1.json": b"private"}),
                                    lambda b: b.update({"subs/clash.yaml": b"empty"}),
                                    lambda b: b.update({"subs/singbox.json": b"{}"}),
                                    lambda b: b.update({"subs/v2ray.txt": b"not base64"})])
def test_input_allowlist_and_formats_fail_before_first_stage(mutation):
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    bodies = copy.deepcopy(BODIES)
    mutation(bodies)
    with pytest.raises(ArtifactError):
        publish.publish_candidate(owner, permit, objects, public, bodies)
    assert objects.writes == [] and owner.store.stages == {}


def test_node_drop_gate_and_wrong_old_release_fail_before_promotion():
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    stage(owner, objects, public, permit, uris=[URI + str(i) for i in range(10)])
    owner.commit_verified(permit, CHECKS["deploy"])
    permit = owner.begin("refresh", OLD, previous=OLD)
    candidate = publish.publish_candidate(owner, permit, objects, public, BODIES)
    before = len(objects.writes)
    with pytest.raises(ArtifactError, match="COUNT_DROP"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=audit(permit, public), root=Path("fixture"))
    assert len(objects.writes) == before


@pytest.mark.parametrize("key", ["_sba/control-v1.json", "a/../b", "a//b", "/a", "a/", "a?x"])
def test_object_adapter_rejects_private_or_unsafe_keys(key):
    with pytest.raises(ArtifactError, match="KEY"):
        ArtifactStore._key(key)


@pytest.mark.parametrize("origin", ["http://fixture.workers.dev/aggregator", "https://example.com/aggregator",
                                     "https://w.a.workers.dev/aggregator?token=x", "https://w.a.workers.dev/aggregator/"])
def test_public_adapter_origin_is_fixed_without_credentials_or_query(origin):
    with pytest.raises(ArtifactError, match="ORIGIN"):
        publish.PublicArtifacts(origin)


def test_yaml_exception_does_not_disclose_subscription_credentials():
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    bodies = copy.deepcopy(BODIES)
    bodies["subs/clash.yaml"] = b'proxies: [password: fixture-private-do-not-log'
    with pytest.raises(ArtifactError, match="FORMAT") as error:
        publish.publish_candidate(owner, permit, objects, public, bodies)
    assert "fixture-private" not in str(error.value)
    assert not objects.writes


@pytest.mark.parametrize("field,value", [("operation_id", "0" * 32), ("candidate_digest", "0" * 64),
                                        ("aggregator_sha", "0" * 40)])
def test_audit_receipt_must_match_operation_bytes_and_approved_gitlink(field, value):
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    candidate = publish.publish_candidate(owner, permit, objects, public, BODIES)
    receipt = replace(audit(permit, public), **{field: value})
    before = len(objects.writes)
    with pytest.raises(ArtifactError, match="AUDIT"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=receipt, root=Path("fixture"))
    assert len(objects.writes) == before


def test_audit_of_another_url_cannot_promote_this_candidate():
    owner, objects, public = setup()
    permit = owner.begin("deploy", OLD)
    candidate = publish.publish_candidate(owner, permit, objects, public, BODIES)
    receipt = audit(permit, public)
    import json
    value = json.loads(receipt.body)
    value["inputs"]["subscriptions"] = ["https://other.invalid/sub"]
    before = len(objects.writes)
    with pytest.raises(ArtifactError, match="AUDIT"):
        publish.promote_candidate(owner, permit, objects, public, candidate,
                                  audit=replace(receipt, body=_encode(value)), root=Path("fixture"))
    assert len(objects.writes) == before


def test_changed_stable_objects_are_not_restored_when_manifest_write_is_unknown():
    owner, objects, public, _, _ = deployed()
    previous = objects.get("manifests/stable.json")
    permit = owner.begin("refresh", OLD, previous=OLD)
    bodies = copy.deepcopy(BODIES)
    bodies["subs/clash.yaml"] += b"# changed\n"
    candidate = publish.publish_candidate(owner, permit, objects, public, bodies)
    receipt = replace(audit(permit, public), candidate_digest=candidate_digest(candidate))
    objects.failed_key = "subs/singbox.json"
    with pytest.raises(ArtifactError, match="WRITE_UNKNOWN"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=receipt, root=Path("fixture"))
    assert objects.get("subs/clash.yaml").body == bodies["subs/clash.yaml"]
    assert objects.get("manifests/stable.json") == previous
    after = len(objects.writes)
    with pytest.raises(ArtifactError, match="STABLE_DRIFT"):
        publish.promote_candidate(owner, permit, objects, public, candidate, audit=receipt, root=Path("fixture"))
    assert len(objects.writes) == after and state(owner.store)["writer"] is not None
