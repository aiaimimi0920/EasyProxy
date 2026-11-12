import contextlib
import importlib.util
import io
import json
import os
import sys
from pathlib import Path
from unittest.mock import Mock, patch

import pytest

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/prefetch-aggregator-issue91.py"
spec = importlib.util.spec_from_file_location("prefetch_issue91", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

SECRET_URL = "https://example.invalid/sub?token=private-test-token"
PAYLOAD = "proxies:\n  - name: private-node\n    type: ss\n"


def runtime(seed=True):
    return {"domains": ([{"name": module.ISSUE91_DOMAIN_NAME, "enable": True, "sub": [SECRET_URL]}] if seed else []),
            "storage": {"items": {"public-effective": {"bucket": "fork-owned-bucket", "key": "subs/effective.txt"}}}}


def run_main(tmp_path, data, *extra):
    config = tmp_path / "runtime.json"
    output = tmp_path / "seed.yaml"
    config.write_text(json.dumps(data), encoding="utf-8")
    stdout = io.StringIO()
    with patch.object(sys, "argv", [str(SCRIPT), "--runtime-config", str(config), "--output", str(output), *extra]), patch.dict(os.environ, {}, clear=True), contextlib.redirect_stdout(stdout):
        result = module.main()
    return result, stdout.getvalue(), config, output


@pytest.mark.parametrize("kind", ["absent", "disabled", "missing-secret"])
def test_optional_seed_skips_without_fetch_or_writes(tmp_path, kind):
    data = runtime(kind != "absent")
    if kind != "absent":
        data["domains"][0]["enable"] = False
    if kind == "missing-secret":
        data["domains"][0]["name"] += " [disabled-missing-secret]"
        data["domains"][0]["sub"] = []
    with patch.object(module, "fetch_text") as fetch, patch.object(module, "build_r2_client") as build:
        result, text, config, output = run_main(tmp_path, data)
    assert result == 0 and json.loads(text)["status"] == "skipped"
    fetch.assert_not_called()
    build.assert_not_called()
    assert not output.exists() and json.loads(config.read_text()) == data


@pytest.mark.parametrize("override", [None, "explicit-cache"])
def test_live_seed_uses_selected_bucket_and_redacts_output(tmp_path, override):
    extra = ("--cache-bucket", override) if override else ()
    with patch.object(module, "fetch_text", return_value=PAYLOAD), patch.object(module, "upload_cached_snapshot") as upload:
        result, text, config, output = run_main(tmp_path, runtime(), *extra)
    upload.assert_called_once_with(PAYLOAD, override or "fork-owned-bucket")
    assert result == 0 and SECRET_URL not in text and "private-node" not in text
    assert output.read_text() == PAYLOAD
    assert json.loads(config.read_text())["domains"][0]["sub"][0].startswith("file:///")


def test_cache_fallback_uses_runtime_bucket(tmp_path):
    with patch.object(module, "fetch_text", side_effect=RuntimeError(SECRET_URL)), patch.object(module, "download_cached_snapshot", return_value=(PAYLOAD, "private-r2-cache")) as download, patch.object(module, "upload_cached_snapshot") as upload:
        result, text, _, _ = run_main(tmp_path, runtime())
    download.assert_called_once_with("fork-owned-bucket")
    upload.assert_not_called()
    assert result == 0 and json.loads(text)["source_mode"] == "cache" and SECRET_URL not in text


def test_fetch_errors_do_not_expose_url_or_response():
    patches = [patch.object(module, name, side_effect=RuntimeError(SECRET_URL + PAYLOAD)) for name in
               ("fetch_text_urllib", "fetch_text_requests", "fetch_text_curl", "fetch_text_curl_cffi")]
    with contextlib.ExitStack() as stack:
        for item in patches:
            stack.enter_context(item)
        with pytest.raises(RuntimeError) as error:
            module.fetch_text(SECRET_URL, 1)
    assert SECRET_URL not in str(error.value) and "private-node" not in str(error.value)
    assert "urllib: RuntimeError" in str(error.value)


def test_ambiguous_bucket_fails_closed():
    data = runtime()
    data["storage"]["items"]["other"] = {"bucket": "other-bucket"}
    with pytest.raises(RuntimeError, match="exactly one"):
        module.cache_bucket(data)


def test_s3_cache_operations_use_selected_bucket():
    client = Mock()
    client.get_object.return_value = {"Body": io.BytesIO(PAYLOAD.encode())}
    with patch.object(module, "build_r2_client", return_value=client):
        assert module.download_cached_snapshot("fork-cache")[0] == PAYLOAD.strip()
        module.upload_cached_snapshot(PAYLOAD, "fork-cache")
    assert client.get_object.call_args.kwargs["Bucket"] == "fork-cache"
    assert client.put_object.call_args.kwargs["Bucket"] == "fork-cache"
