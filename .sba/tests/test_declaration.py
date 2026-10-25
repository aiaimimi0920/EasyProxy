"""声明与实际应用入口、秘密、清单模板保持一致；非线上部署验收。"""
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from request import ACTIONS, APPLICATION_ID, SECRETS
from suite_config import parse_configuration


def test_published_contract_matches_real_entry_actions_and_exact_secret_set():
    root = Path(__file__).resolve().parents[1]
    manifest = json.loads((root / "manifest.json").read_bytes())
    declaration = json.loads((root / "deployment.json").read_bytes())
    assert manifest["schemaVersion"] == 3 and declaration["schemaVersion"] == 2
    assert manifest["id"] == APPLICATION_ID and set(manifest["actions"]) == ACTIONS
    assert manifest["runtime"] == {"runner": "ubuntu-24.04", "powershell": "7.6.9", "python": "3.12", "node": "22"}
    assert set(manifest["secrets"]) == SECRETS and len(manifest["secrets"]) == len(SECRETS)
    assert (root / manifest["entrypoint"]).is_file()
    names = [row["secret"] for row in declaration["accounts"]] + [declaration["github"]["secret"]] + declaration["applicationSecrets"]
    assert len(names) == len(SECRETS) and set(names) == SECRETS
    assert all(0 < value["timeoutSeconds"] <= 3600 for value in manifest["actions"].values())


def test_declaration_templates_produce_the_exact_seven_resource_application_inventory():
    root = Path(__file__).resolve().parents[1]
    value = json.loads((root / "deployment.json").read_bytes())
    render = lambda text: text.replace("{instance}", "testing").replace("{subdomain:runtime}", "fixture-account")
    config = {row["path"][0]: render(row["template"]) for row in value["fields"]}
    config.update(accountId="c" * 32, repository="fixture/easyproxy")
    for row in value["resources"]:
        name = render(row["nameTemplate"])
        config[row["idPath"][0]] = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" if row["kind"] == "d1" else name
        if row["namePath"] is not None:
            config[row["namePath"][0]] = name
    parsed = parse_configuration(config, repository=config["repository"], environment="testing")
    assert [("resource-" + row["key"], row["kind"]) for row in value["resources"]] + [
        ("target-" + str(index), row["kind"]) for index, row in enumerate(value["targets"])] == [
        (row["key"], row["kind"]) for row in parsed.platform_inventory]
    assert len(parsed.platform_inventory) == 7 and len(config) == 11
