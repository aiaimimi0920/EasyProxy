"""产物适配层的真实 boto3/SigV4 请求经本机合成 S3，不访问云。"""
import io
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).parents[1]))
from artifact_store import ArtifactError, ArtifactStore
from test_coordination_s3 import wire


def test_real_object_wire_conditions_metadata_and_full_readback(wire):
    create, record = wire
    objects = ArtifactStore(create())
    first = objects.put("subs/clash.yaml", b"proxies: []\n", None, immutable=True)
    sent = record["writes"][-1]["headers"]
    assert sent["if-none-match"] == "*" and "if-match" not in sent
    assert sent["content-type"] == "application/yaml; charset=utf-8"
    assert sent["cache-control"] == "public, max-age=31536000, immutable"
    second = objects.put("subs/clash.yaml", b"proxies: [fixture]\n", first)
    sent = record["writes"][-1]["headers"]
    assert sent["if-match"] == first.etag and "if-none-match" not in sent
    assert sent["cache-control"] == "no-store" and first.body != second.body
    assert objects.get("subs/clash.yaml") == second
    assert all(row["headers"]["authorization"].startswith("AWS4-HMAC-SHA256 ") for row in record["writes"])
    assert all(method in ("PUT", "GET") for method, _ in record["requests"])


@pytest.mark.parametrize("mode", ["server-error", "response-lost", "missing-etag"])
def test_real_unknown_put_never_replays_or_compensates(mode, wire):
    create, record = wire
    objects = ArtifactStore(create())
    record["mode"] = mode
    with pytest.raises(ArtifactError, match="UNKNOWN") as error:
        objects.put("subs/effective.txt", b"ss://fixture\n", None)
    assert len(record["writes"]) == 1
    assert "private-provider-details" not in str(error.value)
    assert not any(method == "DELETE" for method, _ in record["requests"])


def test_real_sdk_resend_handler_cannot_replay_artifact_put(wire):
    create, record = wire
    store = create()
    objects = ArtifactStore(store)
    store.client.meta.events.register_first("needs-retry.s3.PutObject", lambda attempts, **_: 0 if attempts < 3 else None)
    record["mode"] = "server-error"
    with pytest.raises(ArtifactError, match="WRITE_UNKNOWN"):
        objects.put("subs/effective.txt", b"ss://fixture\n", None)
    assert len(record["writes"]) == 1


@pytest.mark.parametrize("corruption", ["body", "etag", "timeout"])
def test_put_followed_by_bad_readback_sends_no_second_put(corruption, wire, monkeypatch):
    create, record = wire
    store = create()
    original = store.client.get_object

    def get(**kwargs):
        if corruption == "timeout":
            raise TimeoutError("private-provider-details")
        response = original(**kwargs)
        if corruption == "body":
            response["Body"].close()
            response["Body"] = io.BytesIO(b"different")
        else:
            response["ETag"] = '"' + "0" * 32 + '"'
        return response

    monkeypatch.setattr(store.client, "get_object", get)
    with pytest.raises(ArtifactError, match="UNKNOWN") as error:
        ArtifactStore(store).put("subs/effective.json", b'{"fixture":true}', None)
    assert "private-provider-details" not in str(error.value)
    assert len(record["writes"]) == 1
