from unittest.mock import Mock, patch

from scripts.easyproxy_source_audit_probe import (
    discover_directly_usable_nodes,
    management_headers,
    probe_http_proxy,
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
    assert config["geoip"] == {"enabled": False, "database_path": "", "auto_update_enabled": False}
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


def test_direct_probe_results_preserve_source_ref():
    candidates = [
        {
            "tag": "zen-node",
            "name": "Zen Node",
            "source_ref": "manifest:conn_zenproxy_primary",
            "port": 34001,
            "uri": "vless://example",
            "direct_proxy_url": "http://127.0.0.1:34001",
        }
    ]

    with patch(
        "scripts.easyproxy_source_audit_probe.probe_http_proxy",
        return_value={"ok": True, "attempts": []},
    ):
        stable_uris, stable_results = discover_directly_usable_nodes(
            candidates,
            {},
            network_container="audit-container",
        )

    assert stable_uris == ["vless://example"]
    assert stable_results[0]["source_ref"] == "manifest:conn_zenproxy_primary"


def test_proxy_probe_runs_inside_target_container():
    completed = Mock(returncode=0, stdout="204\n", stderr="")
    policy = {
        "http_probe_targets": [
            {
                "url": "https://connectivitycheck.gstatic.com/generate_204",
                "expected_status": [204],
            }
        ]
    }

    with patch(
        "scripts.easyproxy_source_audit_probe.subprocess.run",
        return_value=completed,
    ) as run:
        result = probe_http_proxy(
            "http://127.0.0.1:22323",
            policy,
            network_container="audit-container",
        )

    assert result["ok"] is True
    command = run.call_args.args[0]
    assert command[:5] == [
        "docker",
        "exec",
        "audit-container",
        "python3",
        "-c",
    ]
    assert command[-3:] == [
        "http://127.0.0.1:22323",
        "https://connectivitycheck.gstatic.com/generate_204",
        "25",
    ]


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


def test_proxy_probe_enforces_subprocess_timeout_and_deadline():
    import subprocess

    policy = {
        "http_probe_targets": [
            {
                "url": "https://connectivitycheck.gstatic.com/generate_204",
                "expected_status": [204],
            }
        ]
    }

    # Already-expired deadline returns immediately without spawning docker exec/curl.
    with patch("scripts.easyproxy_source_audit_probe.subprocess.run") as run:
        expired = probe_http_proxy(
            "http://127.0.0.1:22323",
            policy,
            network_container="audit-container",
            deadline=10.0,
        )
    assert expired == {
        "ok": False,
        "attempts": [],
        "error": "audit probe deadline exceeded",
    }
    run.assert_not_called()

    # Subprocess timeout is passed explicitly and converted into a failed attempt instead of hanging forever.
    with (
        patch("scripts.easyproxy_source_audit_probe.time.time", return_value=100.0),
        patch(
            "scripts.easyproxy_source_audit_probe.subprocess.run",
            side_effect=subprocess.TimeoutExpired(cmd=["docker", "exec"], timeout=12.0),
        ) as run,
    ):
        timed_out = probe_http_proxy(
            "http://127.0.0.1:22323",
            policy,
            network_container="audit-container",
            deadline=112.0,
        )

    assert timed_out["ok"] is False
    assert len(timed_out["attempts"]) == 1
    assert "timed out" in timed_out["attempts"][0]["error"].lower()
    assert run.call_args.kwargs["timeout"] == 13


def test_discover_directly_usable_nodes_uses_single_container_batch_exec_when_multi_candidate():
    import json
    import subprocess
    import sys
    from scripts.easyproxy_source_audit_batch import CONTAINER_BATCH_PROXY_PROBE

    # Verify CONTAINER_BATCH_PROXY_PROBE syntax and output contract against an unreachable local port
    batch_input = json.dumps({
        "proxies": ["http://127.0.0.1:1"],
        "targets": [{"url": "http://127.0.0.1:1/generate_204", "expected": [204]}],
        "timeout": 1,
        "accept_below_500": True,
        "retries": 1,
        "retry_delay": 0,
        "budget": 5.0,
        "workers": 4,
    })
    proc = subprocess.run([sys.executable, "-c", CONTAINER_BATCH_PROXY_PROBE], input=batch_input, text=True, capture_output=True, timeout=10, check=True)
    parsed = json.loads(proc.stdout)
    assert parsed["http://127.0.0.1:1"]["ok"] is False

    candidates = [
        {"tag": "n1", "name": "node-1", "source_ref": "sub:1", "port": 34001, "uri": "ss://one", "direct_proxy_url": "http://127.0.0.1:34001", "last_latency_ms": 50},
        {"tag": "n2", "name": "node-2", "source_ref": "sub:1", "port": 34002, "uri": "ss://two", "direct_proxy_url": "http://127.0.0.1:34002", "last_latency_ms": 90},
    ]
    policy = {
        "http_probe_targets": [
            {"url": "https://connectivitycheck.gstatic.com/generate_204", "expected_status": [204]},
            {"url": "https://cp.cloudflare.com/generate_204", "expected_status": [204]},
            {"url": "https://www.google.com/generate_204", "expected_status": [204]},
        ],
        "request_timeout_seconds": 25,
    }
    mock_out = json.dumps({
        "http://127.0.0.1:34001": {"ok": True, "attempts": [{"target": "https://connectivitycheck.gstatic.com/generate_204", "status_code": 204, "exit_code": 0, "ok": True, "stderr": ""}], "winning_target": "https://connectivitycheck.gstatic.com/generate_204", "winning_status": 204},
        "http://127.0.0.1:34002": {"ok": False, "attempts": []},
    })
    with patch("scripts.easyproxy_source_audit_batch.subprocess.run", return_value=Mock(returncode=0, stdout=mock_out, stderr="")) as batch_run:
        uris, results = discover_directly_usable_nodes(candidates, policy, network_container="audit-container")
    assert batch_run.call_count == 1
    sent = json.loads(batch_run.call_args.kwargs["input"])
    assert len(sent["targets"]) == 2 and sent["timeout"] == 10
    assert uris == ["ss://one"] and len(results) == 1 and results[0]["tag"] == "n1"
