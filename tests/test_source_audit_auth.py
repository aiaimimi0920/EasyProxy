from unittest.mock import Mock, patch

from scripts.easyproxy_source_audit_probe import (
    management_headers,
    wait_management_ready,
)
from scripts.easyproxy_source_audit_support import build_config


def test_source_audit_config_has_management_password():
    config = build_config(
        {},
        manifest_url="",
        manifest_token="",
        management_password="audit-secret",
        subscriptions=[],
        proxy_uris=[],
        fallback_subscriptions=[],
        detour_source_refs=["manifest:conn_zenproxy_primary"],
        connectors=[],
        multi_port_base=34000,
    )

    assert config["management"]["listen"] == "0.0.0.0:29888"
    assert config["management"]["password"] == "audit-secret"
    assert config["pool"]["detour_source_refs"] == [
        "manifest:conn_zenproxy_primary"
    ]


def test_management_headers_support_json_requests():
    assert management_headers("audit-secret") == {
        "Authorization": "Bearer audit-secret",
    }
    assert management_headers("audit-secret", json_content=True) == {
        "Authorization": "Bearer audit-secret",
        "Content-Type": "application/json",
    }


def test_management_readiness_uses_bearer_auth():
    response = Mock()
    response.raise_for_status.return_value = None
    response.json.return_value = {"management_enabled": True}

    with patch("scripts.easyproxy_source_audit_probe.requests.get", return_value=response) as get:
        result = wait_management_ready("http://127.0.0.1:29888", 1, "audit-secret")

    assert result == {"management_enabled": True}
    get.assert_called_once_with(
        "http://127.0.0.1:29888/api/settings",
        headers={"Authorization": "Bearer audit-secret"},
        timeout=10,
    )


def test_management_readiness_fails_when_container_exits():
    inspect = Mock(returncode=0, stdout="false 1\n")

    with (
        patch(
            "scripts.easyproxy_source_audit_probe.requests.get",
            side_effect=ConnectionError("connection refused"),
        ),
        patch("scripts.easyproxy_source_audit_probe.subprocess.run", return_value=inspect),
    ):
        try:
            wait_management_ready(
                "http://127.0.0.1:29888",
                180,
                "audit-secret",
                container_name="audit-container",
            )
        except RuntimeError as exc:
            assert str(exc) == (
                "container audit-container exited with code 1 before management API became ready"
            )
        else:
            raise AssertionError("stopped audit container was treated as healthy")
