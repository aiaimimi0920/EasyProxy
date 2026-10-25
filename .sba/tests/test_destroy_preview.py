"""销毁编排的准确清单、preflight、顺序及 UNKNOWN 不重放；不连接云账号。"""
import copy
from pathlib import Path
import sys
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import destroy_preview as destroy
from coordination import Coordinator
from github_environment import variables
from request import SECRETS
from test_coordination import CHECKS, MemoryStore, state
from test_request import fixture, parse


@pytest.fixture
def sandbox(tmp_path, monkeypatch):
    request = parse(fixture("destroy-preview"))
    source_context = fixture("preview")["context"]["source"]
    original = destroy.parse_configuration(source_context["configuration"], repository=request.config.repository,
                                            environment=source_context["environment"])
    stores = {}
    for config, purpose, release in [(request.config, "preview", request.candidate),
                                      (original, "production", {"sourceSha": "c" * 40, "applicationVersion": "1.2.0"})]:
        store = MemoryStore()
        store.account_id, store.bucket = config.account_id, config.bucket
        store.client = SimpleNamespace(close=lambda: None)
        owner = Coordinator(store, config.identity(purpose), config.inventory_digest)
        permit = owner.begin("preview" if purpose == "preview" else "deploy", release)
        owner.commit_verified(permit, CHECKS[permit.action])
        stores[config.bucket] = store
    owner = Coordinator(stores[request.config.bucket], request.config.identity("preview"), request.config.inventory_digest)
    record = {"events": [], "deleted": set(), "fail": None, "drift": False, "bad": None}
    credentials = dict.fromkeys(SECRETS, "fixture-value")
    monkeypatch.setattr(destroy, "inspect_checkout", lambda *_: [])
    monkeypatch.setattr(destroy, "r2_store", lambda _account, bucket, *_: stores[bucket])
    provenance = {"schemaVersion": 1, "previewTaskId": request.context["previewTaskId"], "release": request.candidate,
                  "source": source_context, "snapshot": {"id": "d" * 64, "createdAt": 1}}
    monkeypatch.setattr(destroy, "private_record", lambda _: copy.deepcopy(provenance))

    def read(name):
        record["events"].append(("read", name))
        if record["bad"] == name:
            raise RuntimeError("FIXTURE_PREFLIGHT")

    def delete(name):
        record["events"].append(("delete", name))
        record["deleted"].add(name)
        if record["fail"] == name:
            raise OSError("FIXTURE_UNKNOWN")

    class D1:
        def __init__(self, _account, _token, database):
            self.config = original if database == original.database_id else request.config
            self.path = "/accounts/" + self.config.account_id + "/d1/database/" + database
            self.session, self.token = object(), "fixture-token"

        def identity(self):
            read("source-database" if self.config == original else "preview-database")
            return {"uuid": self.config.database_id, "name": self.config.database_name}

        def close(self):
            pass

    class Edge:
        _token, _session = "fixture-token", object()

        def __init__(self, *_):
            pass

        def settings(self, name):
            assert name == request.config.worker_name
            read("worker")
            return destroy.metadata(owner, request.candidate, credentials["EASYPROXY_ECH_TOKEN"])

        def _request(self, method, path):
            assert method == "DELETE" and path == "scripts/" + request.config.worker_name
            delete("worker")

        def list_workers(self):
            assert "worker" in record["deleted"]
            return [{"id": "unrelated-worker"}]

        def close(self):
            pass

    class Pages:
        _token, _session = "fixture-token", object()
        path = "/accounts/" + request.config.account_id + "/pages/projects/" + request.config.pages_name

        def __init__(self, *_):
            pass

        def project(self):
            if "pages" in record["deleted"]:
                return None
            read("pages")
            return {"id": "11111111-1111-1111-1111-111111111111", "name": request.config.pages_name,
                    "subdomain": request.config.pages_name + ".pages.dev", "production_branch": "main", "source": None,
                    "deployment_configs": destroy.configuration(owner, request.candidate, request.config.pages_name,
                        request.config.database_id, {"ADMIN_PASSWORD": credentials["EASYPROXY_MISUB_ADMIN_PASSWORD"],
                        "COOKIE_SECRET": credentials["EASYPROXY_MISUB_COOKIE_SECRET"], "MANIFEST_TOKEN": credentials["EASYPROXY_MANIFEST_TOKEN"]})}

        def close(self):
            pass

    class GitHub:
        _env = "/repos/fixture/easyproxy/environments/testing"

        def __init__(self, *_):
            pass

        def repository_info(self):
            read("repository")
            return {"id": 1, "defaultBranch": "main"}

        def environment_info(self):
            if "github" in record["deleted"]:
                return None
            read("environment")
            return {"id": 2, "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True},
                    "protection_rules": [{"type": "branch_policy"}]}

        def variables(self):
            read("variables")
            return variables(owner, request.config, 1, 2)

        def secret_names(self):
            read("secrets")
            return set()

        def repository_variable(self, name):
            assert name == destroy.REGISTRY_VARIABLE
            read("registry")
            return None

        def policies(self):
            read("policies")
            return [{"id": 3, "name": "main", "type": "branch"}]

        def _request(self, method, path, **kwargs):
            assert method == "DELETE" and path == self._env and kwargs == {"status": 204, "empty": True}
            delete("github")

        def close(self):
            pass

    def wire(_session, _token, method, path, _require, **kwargs):
        if method == "DELETE":
            delete("pages" if path == Pages.path else "database")
            assert path == Pages.path or path.endswith("/d1/database/" + request.config.database_id)
            return None
        assert method == "GET" and path.endswith("/d1/database") and kwargs["params"]["page"] == 1
        return [{"uuid": "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"}]

    def snapshot(api):
        assert api.config == original
        read("source-snapshot")
        return SimpleNamespace(digest=("e" if record["drift"] and record["deleted"] else "d") * 64)

    def preflight(_owner, permit, output):
        assert not record["deleted"] and permit.action == "destroy-preview" and output.is_absolute()
        read("terminal-location-and-bucket")

    def finish(_owner, permit, output, *, source_unchanged, removed):
        assert record["deleted"] == {"github", "worker", "pages", "database"}
        assert state(stores[request.config.bucket])["writer"]["action"] == "destroy-preview"
        assert source_unchanged()["unchanged"] is True and len(removed) == 7
        delete("bucket")

    for name, value in [("D1API", D1), ("WorkerAPI", Edge), ("PagesAPI", Pages), ("GitHubAPI", GitHub),
                        ("api_result", wire), ("capture", snapshot), ("preflight_bucket", preflight), ("finish_bucket", finish)]:
        monkeypatch.setattr(destroy, name, value)
    root = tmp_path / "source"
    root.mkdir()
    return request, root, tmp_path / "output", credentials, stores, record


def run(sandbox):
    request, root, output, credentials, *_ = sandbox
    return destroy.destroy_preview(request, root, output, credentials=credentials)


def test_exact_seven_resources_and_source_after_upgrade_are_supported(sandbox):
    result = run(sandbox)
    assert result["checks"] == {"preview-removed": True, "source-unchanged": True}
    assert result["lifecycle"]["resources"] == [{"key": row["key"], "status": "removed"} for row in sandbox[0].config.platform_inventory]
    events = sandbox[-1]["events"]
    assert [name for kind, name in events if kind == "delete"] == ["github", "worker", "pages", "database", "bucket"]
    assert events.index(("read", "terminal-location-and-bucket")) < events.index(("delete", "github"))


@pytest.mark.parametrize("bad", ["source-database", "preview-database", "worker", "pages", "repository", "environment",
                                  "variables", "secrets", "registry", "policies", "terminal-location-and-bucket"])
def test_every_preflight_failure_precedes_all_destructive_writes(sandbox, bad):
    sandbox[-1]["bad"] = bad
    with pytest.raises(RuntimeError, match="FIXTURE_PREFLIGHT"):
        run(sandbox)
    assert sandbox[-1]["deleted"] == set()


@pytest.mark.parametrize("name", ["github", "worker", "pages", "database", "bucket"])
def test_unknown_delete_retains_writer_and_repeated_action_does_not_send_again(sandbox, name):
    sandbox[-1]["fail"] = name
    with pytest.raises(OSError, match="FIXTURE_UNKNOWN"):
        run(sandbox)
    before = list(sandbox[-1]["events"])
    with pytest.raises(RuntimeError):
        run(sandbox)
    assert sandbox[-1]["events"] == before
    assert state(sandbox[4][sandbox[0].config.bucket])["writer"]["action"] == "destroy-preview"


def test_source_drift_prevents_bucket_terminal_success(sandbox):
    sandbox[-1]["drift"] = True
    with pytest.raises(RuntimeError, match="REFRESH_REJECTED"):
        run(sandbox)
    assert "bucket" not in sandbox[-1]["deleted"]


def test_existing_output_is_rejected_before_any_provider_read_or_delete(sandbox):
    sandbox[2].mkdir()
    with pytest.raises(RuntimeError, match="REFRESH_REJECTED"):
        run(sandbox)
    assert sandbox[-1]["events"] == []
