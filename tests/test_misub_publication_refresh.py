from pathlib import Path
from copy import deepcopy
import importlib.util
import sys

import pytest
import yaml


ROOT = Path(__file__).resolve().parents[1]


def test_misub_refresh_runs_only_after_stable_verification_and_keeps_extra_providers():
    workflow = yaml.safe_load((ROOT / ".github/workflows/deploy-aggregator.yml").read_text(encoding="utf-8"))
    steps = workflow["jobs"]["deploy"]["steps"]
    names = [step["name"] for step in steps]
    refresh = steps[names.index("Refresh MiSub runtime nodes from verified stable release")]
    assert names.index(refresh["name"]) > names.index("Re-verify canonical stable artifacts")
    assert "always()" not in refresh["if"]
    assert "ADDITIONAL_SUBSCRIPTIONS_JSON" in refresh["env"]
    assert "sync-misub-runtime-sources.py" in refresh["run"]
    assert 'command.extend(["--subscription-url", url.strip()])' in refresh["run"]
    assert "secrets.MISUB_ADMIN_PASSWORD" in refresh["env"]["MISUB_ADMIN_PASSWORD"]


@pytest.fixture
def runtime_sync(monkeypatch):
    spec = importlib.util.spec_from_file_location("runtime_refresh_under_test", ROOT / "scripts/sync-misub-runtime-sources.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    monkeypatch.setenv("MISUB_ADMIN_PASSWORD", "test-admin")
    monkeypatch.setenv("MISUB_MANIFEST_TOKEN", "test-manifest")
    monkeypatch.setenv("MISUB_CRON_SECRET", "test-cron")
    monkeypatch.setattr(sys, "argv", ["sync", "--base-url", "https://misub.example", "--subscription-url", "https://source.example/sub"])
    initial = {
        "misubs": [
            {"id": "proxy_runtime_node_99", "kind": "proxy_uri", "input": "ss://old", "options": {"managed_by": "easyproxy_runtime_sources"}},
            {"id": "user-source", "kind": "subscription", "input": "https://user.example/sub"},
            {"id": "ech", "kind": "connector"},
            {"id": "zen", "kind": "connector"},
        ],
        "profiles": [{"id": "aggregator_global", "customId": "aggregator-global", "manualNodes": ["proxy_runtime_node_99", "ech"]}],
    }

    class Response:
        def __init__(self, value): self.value = value
        status_code = 200
        def raise_for_status(self): pass
        def json(self): return deepcopy(self.value)

    class Session:
        data_reads = 0
        concurrent_edit = False
        stale_manifest = False
        saved = None
        cron = None
        def get(self, url, **kwargs):
            if url.endswith("api/data"):
                self.data_reads += 1
                data = deepcopy(initial)
                if self.concurrent_edit and self.data_reads > 1:
                    data["misubs"].append({"id": "new-user-edit"})
                return Response(data)
            if url.endswith("api/settings"):
                return Response({"aggregatorSync": {"defaultPublicProfileConnectorIds": ["zen"], "sourceUrl": "https://discovery.example"}})
            if "/api/manifest/" in url:
                sources = self.saved["misubs"] if not self.stale_manifest else initial["misubs"]
                return Response({"success": True, "sources": [s for s in sources if s["kind"] != "subscription"]})
            raise AssertionError(url)
        def post(self, url, **kwargs):
            if url.endswith("api/misubs"): self.saved = deepcopy(kwargs["json"])
            elif url.endswith("api/settings"): self.cron = deepcopy(kwargs["json"])
            elif not url.endswith("api/login"): raise AssertionError(url)
            return Response({"success": True})

    session = Session()
    monkeypatch.setattr(module.requests, "Session", lambda: session)
    monkeypatch.setattr(module, "run_runtime_audit", lambda **kwargs: {"audit_id": "fresh-audit", "nodes": {"stable_available_uris": ["ss://fresh", "ss://fresh"]}})
    return module, session


def test_runtime_refresh_preserves_connectors_and_unmanaged_sources(runtime_sync, capsys):
    module, session = runtime_sync
    assert module.main() == 0
    ids = {source["id"] for source in session.saved["misubs"]}
    assert "user-source" in ids and "proxy_runtime_node_99" not in ids
    assert session.saved["profiles"][0]["manualNodes"] == ["proxy_runtime_node_1", "zen", "ech"]
    assert session.cron["aggregatorSync"]["runOnCron"] is False
    assert session.cron["aggregatorSync"]["sourceUrl"] == "https://discovery.example"
    assert "ss://fresh" not in capsys.readouterr().out


def test_runtime_refresh_refuses_concurrent_user_edits(runtime_sync):
    module, session = runtime_sync
    session.concurrent_edit = True
    with pytest.raises(RuntimeError, match="concurrent edits"):
        module.main()
    assert session.saved is None


def test_runtime_refresh_rejects_stale_manifest(runtime_sync):
    module, session = runtime_sync
    session.stale_manifest = True
    with pytest.raises(RuntimeError, match="newly saved proxy nodes"):
        module.main()


def test_empty_audit_does_not_replace_existing_nodes(runtime_sync, monkeypatch):
    module, session = runtime_sync
    monkeypatch.setattr(module, "run_runtime_audit", lambda **kwargs: {"nodes": {"stable_available_uris": []}})
    with pytest.raises(RuntimeError, match="no stable proxy URIs"):
        module.main()
    assert session.saved is None
