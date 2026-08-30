import importlib.util
import os
import sys
from pathlib import Path
from unittest.mock import Mock, patch

import requests


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "verify-misub-deploy.py"


def load_script():
    spec = importlib.util.spec_from_file_location("verify_misub_deploy", SCRIPT)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def response(*, payload=None, text="", error=None):
    result = Mock(status_code=200, text=text)
    result.json.return_value = payload
    result.raise_for_status.side_effect = error
    return result


def test_main_retries_transient_public_config_failure():
    module = load_script()
    session = Mock()
    session.get.side_effect = [
        response(text="<html></html>"),
        response(error=requests.HTTPError("500 Server Error")),
        response(payload={"enablePublicPage": True}),
        response(payload={"mytoken": "configured"}),
        response(payload={"success": True, "sources": [{"id": "source"}]}),
        response(payload={"ok": True}),
    ]
    session.post.return_value = response(payload={"success": True})

    with (
        patch.object(module.requests, "Session", return_value=session),
        patch.object(module.time, "sleep"),
        patch.object(sys, "argv", [str(SCRIPT), "--base-url", "https://misub.example"]),
        patch.dict(
            os.environ,
            {"MISUB_ADMIN_PASSWORD": "admin-secret", "MISUB_MANIFEST_TOKEN": "manifest-secret"},
        ),
    ):
        assert module.main() == 0

    assert session.get.call_count == 6
