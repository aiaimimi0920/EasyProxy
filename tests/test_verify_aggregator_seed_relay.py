import importlib.util
from pathlib import Path
from unittest.mock import Mock, patch

import pytest
import requests


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "verify-aggregator-seed-relay.py"
spec = importlib.util.spec_from_file_location("verify_seed_relay", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def test_verifies_nonempty_proxy_list():
    response = Mock(text="proxies:\n  - name: example\n")
    with patch.object(module.requests, "get", return_value=response) as fetch:
        assert module.verify("https://relay.invalid/", 12) == {
            "relay_url": "https://relay.invalid/issue91", "proxy_count": 1,
        }
    fetch.assert_called_once_with("https://relay.invalid/issue91", timeout=12)
    response.raise_for_status.assert_called_once()


@pytest.mark.parametrize("body", ["[]", "proxies: {}", "proxies: []"])
def test_rejects_missing_or_empty_proxy_list(body):
    with patch.object(module.requests, "get", return_value=Mock(text=body)):
        with pytest.raises(RuntimeError):
            module.verify("https://relay.invalid", 12)


def test_invalid_yaml_does_not_disclose_subscription_body():
    response = Mock(text="proxies: [private-subscription-token\n")
    with patch.object(module.requests, "get", return_value=response):
        with pytest.raises(RuntimeError, match="invalid YAML") as error:
            module.verify("https://relay.invalid", 12)
    assert "private-subscription-token" not in str(error.value)


def test_network_error_does_not_disclose_url():
    with patch.object(module.requests, "get", side_effect=requests.ConnectionError(
        "request failed https://relay.invalid/private-token/issue91",
    )):
        with pytest.raises(RuntimeError, match="request failed") as error:
            module.verify("https://relay.invalid/private-token", 12)
    assert "private-token" not in str(error.value)


def test_http_error_preserves_status_without_disclosing_url():
    response = Mock(status_code=503)
    response.raise_for_status.side_effect = requests.HTTPError(
        "503 https://relay.invalid/private-token/issue91", response=response,
    )
    with patch.object(module.requests, "get", return_value=response):
        with pytest.raises(RuntimeError, match="HTTP 503") as error:
            module.verify("https://relay.invalid/private-token", 12)
    assert "private-token" not in str(error.value)
