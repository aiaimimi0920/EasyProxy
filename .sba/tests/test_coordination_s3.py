"""使用真实 boto3/SigV4/HTTP 序列化；只把测试传输送往本机合成 S3。"""
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import socket
import sys
import threading
from urllib.parse import urlsplit

import pytest

spec = importlib.util.spec_from_file_location("coordination", Path(__file__).parents[1] / "coordination.py")
if spec.name not in sys.modules:
    coordination = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = coordination
    spec.loader.exec_module(coordination)
coordination = sys.modules[spec.name]

ACCOUNT = "c" * 32
BUCKET = "easyproxy-testing"
IDENTITY = {"repository": "fixture/easyproxy", "environment": "testing", "accountId": ACCOUNT,
            "bucket": BUCKET, "purpose": "production"}
RELEASE = {"sourceSha": "a" * 40, "applicationVersion": "1.0.0"}


@pytest.fixture
def wire():
    record = {"body": None, "etag": None, "writes": [], "mode": "normal", "requests": [], "backups": {}, "stages": {}}
    mutex = threading.Lock()

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_args):
            pass

        def send(self, status, body=b"", **headers):
            self.send_response(status)
            self.send_header("Content-Length", str(len(body)))
            for key, value in headers.items():
                self.send_header(key, value)
            self.end_headers()
            self.wfile.write(body)

        def error(self, status, code):
            self.send(status, ("<Error><Code>" + code + "</Code><Message>private-provider-details</Message></Error>").encode(),
                      **{"Content-Type": "application/xml"})

        def do_GET(self):
            with mutex:
                record["requests"].append(("GET", self.path))
                if record["mode"] == "missing-bucket":
                    self.error(404, "NoSuchBucket")
                elif "/_sba/backups/" in self.path or "/_sba/operations/" in self.path:
                    objects = record["backups"] if "/_sba/backups/" in self.path else record["stages"]
                    body = objects.get(self.path)
                    if body is None:
                        self.error(404, "NoSuchKey")
                    else:
                        self.send(200, body + (b"corruption" if record["mode"] == "backup-corrupt" else b""))
                elif "list-type=2" in self.path:
                    count = 0 if record["body"] is None else 1
                    contents = "" if not count else "<Contents><Key>_sba/control-v1.json</Key></Contents>"
                    self.send(200, (f"<ListBucketResult><KeyCount>{count}</KeyCount><IsTruncated>false</IsTruncated>"
                                    + contents + "</ListBucketResult>").encode(), **{"Content-Type": "application/xml"})
                elif record["body"] is None:
                    self.error(404, "NoSuchKey")
                else:
                    self.send(200, record["body"], ETag=record["etag"])

        def do_PUT(self):
            def read_chunks(stream):
                chunks = []
                while True:
                    length = int(stream.readline().split(b";", 1)[0].strip(), 16)
                    if not length:
                        while stream.readline().strip():
                            pass
                        return b"".join(chunks)
                    chunks.append(stream.read(length))
                    assert stream.read(2) == b"\r\n"

            body = (read_chunks(self.rfile) if self.headers.get("Transfer-Encoding") == "chunked"
                    else self.rfile.read(int(self.headers["Content-Length"])))
            if self.headers.get("Content-Encoding") == "aws-chunked":
                import io
                body = read_chunks(io.BytesIO(body))
            with mutex:
                headers = dict((key.lower(), value) for key, value in self.headers.items())
                record["requests"].append(("PUT", self.path))
                record["writes"].append({"body": body, "headers": headers})
                if record["mode"] == "server-error":
                    self.error(500, "InternalError")
                    return
                if record["mode"] == "precondition":
                    self.error(412, "PreconditionFailed")
                    return
                if "/_sba/backups/" in self.path or "/_sba/operations/" in self.path:
                    objects = record["backups"] if "/_sba/backups/" in self.path else record["stages"]
                    if headers.get("if-none-match") != "*" or self.path in objects:
                        self.error(412, "PreconditionFailed")
                        return
                    objects[self.path] = body
                    if record["mode"] == "response-lost":
                        self.close_connection = True
                        self.connection.shutdown(socket.SHUT_RDWR)
                        return
                    self.send(200, **({} if record["mode"] == "missing-etag" else {"ETag": '"' + hashlib.md5(body).hexdigest() + '"'}))
                    return
                matches = ((headers.get("if-none-match") == "*" and record["body"] is None)
                           or (headers.get("if-match") == record["etag"] and record["etag"] is not None))
                if not matches:
                    self.error(412, "PreconditionFailed")
                    return
                record["body"] = body
                record["etag"] = '"' + hashlib.md5(body).hexdigest() + '"'
                if record["mode"] == "response-lost":
                    self.close_connection = True
                    self.connection.shutdown(socket.SHUT_RDWR)
                    return
                self.send(200, **({} if record["mode"] == "missing-etag" else {"ETag": record["etag"]}))

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    stores = []

    def new_store():
        store = coordination.r2_store(ACCOUNT, BUCKET, "fixture-access-key", "fixture-secret-key")
        client = store.client
        client._endpoint.http_session._proxy_config._proxies = {}
        original_send = client._endpoint.http_session.send

        def local_send(request):
            target = urlsplit(request.url)
            assert target.scheme == "https"
            assert target.netloc == f"{ACCOUNT}.r2.cloudflarestorage.com"
            assert target.path.startswith("/" + BUCKET)
            request.url = f"http://127.0.0.1:{server.server_port}" + target.path + ("?" + target.query if target.query else "")
            return original_send(request)

        client._endpoint.http_session.send = local_send
        stores.append(store)
        return store

    yield new_store, record
    for store in stores:
        store.client.close()
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
    assert not thread.is_alive()


def test_real_s3_request_uses_conditional_headers_and_never_delete(wire):
    create, record = wire
    store = create()
    owner = coordination.Coordinator(store, IDENTITY, "d" * 64)
    permit = owner.begin("deploy", RELEASE)
    assert record["writes"][0]["headers"]["if-none-match"] == "*"
    assert "if-match" not in record["writes"][0]["headers"]
    owner.commit_verified(permit, {"suite-published": True, "service-ready": True})
    assert record["writes"][1]["headers"]["if-match"] == permit.etag
    assert "if-none-match" not in record["writes"][1]["headers"]
    assert all(item["headers"]["authorization"].startswith("AWS4-HMAC-SHA256 ") for item in record["writes"])
    assert not any("x-amz-security-token" in item["headers"] for item in record["writes"])
    assert all(method in ("GET", "PUT") for method, _ in record["requests"])
    assert json.loads(record["body"])["active"] == RELEASE


@pytest.mark.parametrize("mode", ["server-error", "response-lost", "missing-etag"])
def test_transport_error_or_response_loss_sends_at_most_one_put(mode, wire):
    create, record = wire
    owner = coordination.Coordinator(create(), IDENTITY, "d" * 64)
    record["mode"] = mode
    with pytest.raises(coordination.CoordinationError, match="UNKNOWN") as error:
        owner.begin("deploy", RELEASE)
    assert "private-provider-details" not in str(error.value)
    assert len(record["writes"]) == 1
    if mode != "server-error":
        assert json.loads(record["body"])["writer"] is not None
        record["mode"] = "normal"
        with pytest.raises(coordination.CoordinationError, match="BUSY"):
            coordination.Coordinator(create(), IDENTITY, "d" * 64).begin("deploy", RELEASE)
        assert len(record["writes"]) == 1


def test_single_send_guard_overrides_sdk_retry_handlers(wire):
    create, record = wire
    store = create()
    store.client.meta.events.register_first("needs-retry.s3.PutObject", lambda attempts, **_kwargs: 0 if attempts < 3 else None)
    record["mode"] = "server-error"
    with pytest.raises(coordination.StoreUnknown):
        store.compare_and_set(b"{}", None)
    assert len(record["writes"]) == 1


def test_definitive_precondition_failure_does_not_replay(wire):
    create, record = wire
    record["mode"] = "precondition"
    with pytest.raises(coordination.CoordinationError, match="CONFLICT"):
        coordination.Coordinator(create(), IDENTITY, "d" * 64).begin("deploy", RELEASE)
    assert len(record["writes"]) == 1
    assert record["body"] is None


def test_missing_bucket_is_not_treated_as_an_empty_unowned_key(wire):
    create, record = wire
    record["mode"] = "missing-bucket"
    with pytest.raises(coordination.CoordinationError, match="UNKNOWN"):
        coordination.Coordinator(create(), IDENTITY, "d" * 64).begin("deploy", RELEASE)
    assert record["writes"] == []


def test_stale_etag_cannot_unlock_current_writer_over_real_http(wire):
    create, record = wire
    store = create()
    old_etag = store.compare_and_set(b'{"fixture":1}', None)
    current_etag = create().compare_and_set(b'{"fixture":2}', old_etag)
    with pytest.raises(coordination.StoreConflict):
        store.compare_and_set(b'{"fixture":3}', old_etag)
    assert record["etag"] == current_etag
    assert record["body"] == b'{"fixture":2}'


def test_stage_is_persistent_create_only_and_does_not_change_writer(wire):
    create, record = wire
    owner = coordination.Coordinator(create(), IDENTITY, "d" * 64)
    permit = owner.begin("deploy", RELEASE)
    before = record["body"], record["etag"]
    owner.claim_stage(permit, "ech-publication")
    assert (record["body"], record["etag"]) == before
    assert len(record["stages"]) == 1
    marker = json.loads(next(iter(record["stages"].values())))
    assert marker == {"operationId": permit.operation_id, "stage": "ech-publication",
                      "permitSha256": hashlib.sha256(permit.state_bytes).hexdigest()}
    with pytest.raises(coordination.CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        owner.claim_stage(permit, "ech-publication")
    assert len(record["stages"]) == 1 and len(record["writes"]) == 2
    assert record["writes"][-1]["headers"]["if-none-match"] == "*"
    copied = coordination.Permit(permit.operation_id, permit.action, permit.etag, permit.state_bytes)
    with pytest.raises(coordination.CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        coordination.Coordinator(create(), IDENTITY, "d" * 64).claim_stage(copied, "ech-publication")
    assert len(record["stages"]) == 1 and len(record["writes"]) == 3


@pytest.mark.parametrize("mode", ["server-error", "response-lost", "missing-etag"])
def test_unknown_stage_claim_never_retries_or_unlocks(wire, mode):
    create, record = wire
    owner = coordination.Coordinator(create(), IDENTITY, "d" * 64)
    permit = owner.begin("deploy", RELEASE)
    record["mode"] = mode
    with pytest.raises(coordination.CoordinationError, match="UNKNOWN"):
        owner.claim_stage(permit, "ech-publication")
    assert len(record["writes"]) == 2 and json.loads(record["body"])["writer"] is not None
    record["mode"] = "normal"
    with pytest.raises(coordination.CoordinationError, match="STAGE_ALREADY_ATTEMPTED"):
        owner.claim_stage(permit, "ech-publication")
    assert len(record["writes"]) == 2


def test_factory_rejects_unsafe_endpoint_id_and_implicit_credentials_before_network():
    for account, bucket, access, secret in [
        ("evil.example/", BUCKET, "access", "secret"), (ACCOUNT, "other/bucket", "access", "secret"),
        (ACCOUNT, BUCKET, "", "secret"), (ACCOUNT, BUCKET, "access", "bad\nsecret"),
    ]:
        with pytest.raises(coordination.CoordinationError, match="IDENTITY"):
            coordination.r2_store(account, bucket, access, secret)


def test_ambient_aws_endpoint_token_and_retry_configuration_cannot_change_scope(wire, monkeypatch):
    create, record = wire
    monkeypatch.setenv("AWS_ENDPOINT_URL_S3", "http://foreign.invalid")
    monkeypatch.setenv("AWS_SESSION_TOKEN", "foreign-session-token")
    monkeypatch.setenv("AWS_MAX_ATTEMPTS", "9")
    store = create()
    assert store.client.meta.config.retries["total_max_attempts"] == 1
    store.compare_and_set(b"{}", None)
    assert len(record["writes"]) == 1
    headers = record["writes"][0]["headers"]
    assert "x-amz-security-token" not in headers
    assert "foreign-session-token" not in str(headers)
