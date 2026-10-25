"""公开配置只有资源身份；不接受秘密、跨实例目标或其他仓库的写目标。"""
import copy
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from suite_config import ConfigurationError, parse_configuration
from coordination import Coordinator
from test_coordination import MemoryStore


PUBLIC = {"accountId": "c" * 32, "repository": "Fixture/EasyProxy", "pagesName": "ep-testing-misub",
    "workerName": "ep-testing-edge", "workersSubdomain": "fixture", "databaseId": "12345678-1234-1234-1234-123456789abc",
    "databaseName": "ep-testing-misub", "bucketName": "ep-testing-store",
    "refreshTarget": ".github/workflows/sba-aggregator-refresh.yml@testing",
    "pagesOrigin": "https://ep-testing-misub.pages.dev", "workerOrigin": "https://ep-testing-edge.fixture.workers.dev"}


def test_public_configuration_derives_same_suite_identity_and_canonical_github_repository():
    value = parse_configuration(PUBLIC, repository="fixture/EasyProxy", environment="testing")
    assert value.repository == "fixture/easyproxy"
    assert value.pages_origin == "https://ep-testing-misub.pages.dev"
    assert value.worker_origin == "https://ep-testing-edge.fixture.workers.dev"
    assert value.identity("production")["purpose"] == "production"
    assert {row["kind"] for row in value.inventory} == {"d1", "r2", "pages", "worker", "github-actions"}
    assert len(value.inventory_digest) == 64
    assert parse_configuration(value.configuration(), repository=value.repository, environment="testing") == value
    assert len(value.platform_inventory) == 7
    # The result is directly usable by the existing persistent Coordinator contract.
    store = MemoryStore()
    store.bucket = value.bucket
    Coordinator(store, value.identity("production"), value.inventory_digest)


@pytest.mark.parametrize("mutation", [lambda c: c.update(token="must-not-enter-public-config"),
    lambda c: c.update(repository="foreign/EasyProxy"), lambda c: c.update(accountId="account"),
    lambda c: c.update(databaseId="not-a-uuid"), lambda c: c.update(pagesName="misub-git"),
    lambda c: c.update(workerName="proxyservice-ech-workers"), lambda c: c.update(databaseName="misub"),
    lambda c: c.update(bucketName="aggregator"), lambda c: c.update(workersSubdomain="user@host"),
    lambda c: c.update(refreshTarget=".github/workflows/deploy-aggregator.yml@testing"),
    lambda c: c.update(refreshTarget=".github/workflows/sba-aggregator-refresh.yml@production"),
    lambda c: c.update(pagesOrigin="https://foreign.pages.dev"),
    lambda c: c.update(workerOrigin="https://ep-testing-edge.foreign.workers.dev"),
    lambda c: c.update(pagesName=True), lambda c: c.pop("databaseId")])
def test_unknown_keys_old_manual_resources_and_identity_drift_are_rejected(mutation):
    raw = copy.deepcopy(PUBLIC)
    mutation(raw)
    with pytest.raises(ConfigurationError):
        parse_configuration(raw, repository="fixture/EasyProxy", environment="testing")


@pytest.mark.parametrize("environment", ["Testing", "../testing", "testing\n", "a" * 63])
def test_unrepresentable_environment_is_rejected_instead_of_truncating_resource_names(environment):
    with pytest.raises(ConfigurationError):
        parse_configuration(PUBLIC, repository="fixture/EasyProxy", environment=environment)


def test_resource_lists_are_not_mutable_shared_state():
    value = parse_configuration(PUBLIC, repository="fixture/EasyProxy", environment="testing")
    digest = value.inventory_digest
    value.inventory[0]["id"] = "changed"
    assert value.inventory_digest == digest
