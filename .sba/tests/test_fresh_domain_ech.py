"""真实 esbuild/HTTP Worker恢复：同bundle单次PUT，不重发subdomain或域名attach。"""
from dataclasses import replace
import hashlib
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import fresh_domain_resources as resources
from fresh_domain_resume import FreshDomainResume
from repair_context import FRESH_DOMAIN_PARENT_RELEASE as PARENT
from test_ech import candidate, api_wire, checkout, held, ech, ECH_TOKEN


@pytest.mark.parametrize("failure", [None, "upload", "domain-read", "domain-proof"])
def test_existing_custom_worker_is_updated_once_without_other_provider_mutations(candidate, api_wire, monkeypatch, tmp_path, failure):
    release, settings = candidate
    release = {**release, "applicationVersion": "1.0.13"}
    origin = "https://edge.example.com"
    settings = replace(settings, inventory=[*settings.inventory, {"kind": "domain", "name": settings.worker_name, "id": origin}])
    owner, permit, store, _ = held(release, inventory=settings.inventory)
    resume = FreshDomainResume(permit.operation_id, hashlib.sha256(permit.state_bytes).hexdigest(), "fixture-account")
    bundle = ech.build_bundle(settings.root, settings.esbuild, tmp_path / "parent-bundle")
    assert hashlib.sha256(bundle).hexdigest() == resources.WORKER_BUNDLE_SHA256
    api_wire.update(settings=ech.metadata(owner, PARENT, ECH_TOKEN), downloadBundle=bundle, customHost="edge.example.com")
    reads = []
    def domain(*_):
        reads.append("domain")
        if failure == "domain-read": raise RuntimeError("FIXTURE_READ_FAILURE")
    def proof(current, held_permit, *_):
        reads.append("proof")
        if failure == "domain-proof": raise RuntimeError("FIXTURE_PROOF_FAILURE")
        current.claim_stage(held_permit, "worker-domain-verification")
    monkeypatch.setattr(resources, "verify_worker_domain", domain)
    monkeypatch.setattr(resources, "record_worker_domain", proof)
    if failure == "upload": api_wire["mode"] = "server-error"
    run = lambda: ech.publish_ech(owner, permit, settings, api_token="fixture-api-token", ech_token=ECH_TOKEN, resume=resume)
    if failure:
        with pytest.raises(RuntimeError): run()
    else:
        assert run()["origin"] == origin and reads == ["domain", "proof"]
        assert set(api_wire["publicHosts"]) == {settings.worker_name + ".fixture-account.workers.dev", "edge.example.com"}
        assert api_wire["bundle"] == bundle
    assert len(api_wire["writes"]) == (0 if failure == "domain-read" else 1)
    assert all(method == "PUT" and path.endswith("/workers/scripts/" + settings.worker_name)
               for method, path, *_ in api_wire["writes"])
