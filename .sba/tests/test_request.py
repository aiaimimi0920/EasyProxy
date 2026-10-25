"""应用只读请求边界；部署前拒绝漂移与过宽清单，verify 不读取任何秘密。"""
import copy
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from request import ApplicationRequest, RequestError, SECRETS, parse_request
from suite_config import parse_configuration
from test_suite_config import PUBLIC


def fixture(action="deploy"):
    config = copy.deepcopy(PUBLIC)
    value = {"schemaVersion": 3, "taskId": "dc-" + "1" * 32, "action": action, "repository": "Fixture/EasyProxy",
             "sourceSha": "b" * 40, "applicationId": "easyproxy-cloud", "applicationVersion": "1.1.0",
             "environment": "testing", "configuration": config, "previous": None}
    if action != "deploy":
        value["previous"] = {"sourceSha": "a" * 40, "applicationVersion": "1.0.0"} if action in ("update", "preview") else {
            "sourceSha": value["sourceSha"], "applicationVersion": value["applicationVersion"]}
    if action == "preview":
        original = copy.deepcopy(config)
        for key in ("pagesName", "workerName", "databaseName", "bucketName", "refreshTarget", "pagesOrigin", "workerOrigin"):
            original[key] = original[key].replace("testing", "production")
        original["databaseId"] = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
        parsed = parse_configuration(original, repository=value["repository"], environment="production")
        value["context"] = {"source": {"instanceId": "dc-" + "2" * 32, "taskId": "dc-" + "3" * 32,
            **value["previous"], "environment": "production", "configuration": original,
            "resources": parsed.platform_inventory, "resultDigest": "d" * 64},
            "resources": parse_configuration(config, repository=value["repository"], environment="testing").platform_inventory,
            "urls": [config["workerOrigin"], config["pagesOrigin"]]}
    elif action == "destroy-preview":
        value["context"] = {"instanceId": "dc-" + "2" * 32, "previewTaskId": "dc-" + "3" * 32,
            "resultDigest": "d" * 64,
            "resources": parse_configuration(config, repository=value["repository"], environment="testing").platform_inventory}
    return value


def parse(value):
    return parse_request(json.dumps(value, ensure_ascii=True).encode(), application_version="1.1.0")


@pytest.mark.parametrize("action", ["deploy", "update", "verify", "preview", "destroy-preview"])
def test_all_five_actions_map_to_explicit_configuration_without_side_effects(action):
    result = parse(fixture(action))
    assert type(result) is ApplicationRequest and result.action == action
    assert result.config.repository == "fixture/easyproxy"
    assert result.candidate == {"sourceSha": "b" * 40, "applicationVersion": "1.1.0"}


@pytest.mark.parametrize("mutation", [lambda v: v.update(schemaVersion=True), lambda v: v.update(schemaVersion=2),
    lambda v: v.update(applicationId="foreign"), lambda v: v.update(applicationVersion="2.0.0"),
    lambda v: v.update(sourceSha="main"), lambda v: v.update(repository="other/repo"),
    lambda v: v.update(environment="other"), lambda v: v.update(action="repair"),
    lambda v: v.update(resources=[]), lambda v: v.update(context={}), lambda v: v.pop("taskId"),
    lambda v: v["configuration"].update(password="secret"), lambda v: v.update(previous={})])
def test_deploy_identity_unknown_fields_and_secret_in_public_configuration_are_rejected(mutation):
    value = fixture()
    mutation(value)
    with pytest.raises(RequestError):
        parse(value)


@pytest.mark.parametrize("action", ["update", "preview", "verify", "destroy-preview"])
def test_previous_release_must_match_action_version_rules(action):
    value = fixture(action)
    value["previous"] = {"sourceSha": value["sourceSha"], "applicationVersion": "1.0.0"}
    with pytest.raises(RequestError):
        parse(value)


@pytest.mark.parametrize("raw", [b"", b"{}", b"null", b"[]", b"\xef\xbb\xbf{}", b"\xff", b'{"action":NaN}',
    b'{"action":"deploy","action":"verify"}', b" " * 49153],
    ids=["empty", "object", "null", "array", "bom", "invalid-utf8", "nan", "duplicate", "oversized"])
def test_malformed_ambiguous_nonfinite_and_oversized_json_rejected(raw):
    with pytest.raises(RequestError):
        parse_request(raw, application_version="1.1.0")


@pytest.mark.parametrize("mutation", [lambda c: c["resources"].pop(),
    lambda c: c["resources"].append(copy.deepcopy(c["resources"][0])),
    lambda c: c["resources"][0].update(remoteId="cccccccc-cccc-cccc-cccc-cccccccccccc"),
    lambda c: c["resources"][0].update(accountId="e" * 32),
    lambda c: c["resources"][-1].update(remoteId="https://foreign.pages.dev"),
    lambda c: c["source"].update(environment="testing"),
    lambda c: c["source"].update(resultDigest="invalid"),
    lambda c: c["source"]["configuration"].update(databaseId=PUBLIC["databaseId"]),
    lambda c: c.update(urls=["https://foreign.pages.dev"]),
    lambda c: c.update(secrets={})])
def test_preview_exact_inventory_source_and_side_effect_isolation_are_required(mutation):
    value = fixture("preview")
    mutation(value["context"])
    with pytest.raises(RequestError):
        parse(value)


def test_verify_never_reads_even_existing_environment_placeholders():
    class NoSecretAccess(dict):
        def get(self, *_):
            raise AssertionError("verify must not inspect secrets")

    assert parse(fixture("verify")).credentials(NoSecretAccess()) == {}


def test_nonverify_credentials_only_select_declared_names_and_require_real_single_line_values():
    request = parse(fixture())
    values = {name: "fixture-value" for name in SECRETS}
    assert request.credentials({**values, "GITHUB_TOKEN": "must-not-be-inherited"}) == values
    for invalid in (None, "", "verification-not-applicable", "line\nbreak", "x" * 513, "\ud800"):
        with pytest.raises(RequestError):
            request.credentials({**values, "EASYPROXY_ECH_TOKEN": invalid})
