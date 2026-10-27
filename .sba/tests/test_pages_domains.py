"""Pages 自定义公开地址的跨阶段发布合同，复用原严格 HTTP wire。"""
from dataclasses import replace

import pytest

from test_pages import (candidate, api_wire, held, pages, project, deployment, publish, state_writer,
                        PROJECT, DATABASE_ID, SECRETS, StageError)


@pytest.mark.parametrize("action", ["deploy", "update", "unknown"])
def test_custom_pages_urls_publish_and_verify_without_hiding_domain_unknown(candidate, api_wire, monkeypatch, action):
    release, settings = candidate
    origin = "https://app.example.com"
    inventory = [*settings.inventory, {"kind": "domain", "name": PROJECT, "id": origin}]
    settings = replace(settings, inventory=inventory)
    owner, permit, store, old = held(release, "update" if action == "update" else "deploy", inventory)
    api_wire["customHost"] = "app.example.com"
    if action == "update":
        api_wire["project"] = project(pages.configuration(owner, old, PROJECT, DATABASE_ID, SECRETS, origin=origin), deployment(old))
    calls = []

    def bind(current, permission, selected, service, kind, token):
        assert current is owner and permission is permit and selected == inventory
        assert (service, kind, token) == (PROJECT, "pages", "fixture-api-token")
        assert api_wire["publicHosts"] == [PROJECT + ".pages.dev"]
        owner.assert_owned(permit)
        calls.append("bind")
        if action == "unknown": raise RuntimeError("FIXTURE_DOMAIN_UNKNOWN")

    monkeypatch.setattr(pages, "bind_domain", bind)
    if action == "unknown":
        with pytest.raises(StageError, match="DOMAINS_UNKNOWN"): publish(owner, permit, settings)
        assert api_wire["publicHosts"] == [PROJECT + ".pages.dev"]
    else:
        assert publish(owner, permit, settings)["origin"] == origin
        assert api_wire["publicHosts"] == [PROJECT + ".pages.dev", "app.example.com"]
    env = api_wire["project"]["deployment_configs"]["production"]["env_vars"]
    assert env["MISUB_PUBLIC_URL"]["value"] == env["MISUB_CALLBACK_URL"]["value"] == origin
    assert calls == ["bind"] and state_writer(store) == permit.operation_id
