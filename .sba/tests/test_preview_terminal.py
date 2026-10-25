"""同址控制 bucket 的终态顺序/UNKNOWN 不重放；本机内存 S3，非云清理。"""
import hashlib
import json
from pathlib import Path
import sys
import threading
from types import SimpleNamespace

from botocore.exceptions import ClientError
import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from coordination import Coordinator, STATE_KEY
from preview_terminal import finish_bucket, preflight_bucket
from refresh_context import RefreshError
from test_coordination import CHECKS, IDENTITY, MemoryStore, NEW, state


class Events:
    def __init__(self):
        self.handlers = {}

    def register_first(self, event, handler, unique_id):
        assert (event, unique_id) not in self.handlers
        self.handlers[(event, unique_id)] = handler

    def unregister(self, event, unique_id):
        del self.handlers[(event, unique_id)]

    def emit(self, event):
        for (name, _), handler in self.handlers.items():
            if name == event:
                handler()


@pytest.fixture
def terminal():
    store = MemoryStore()
    owner = Coordinator(store, {**IDENTITY, "purpose": "preview"}, "d" * 64)
    initial = owner.begin("preview", NEW)
    owner.commit_verified(initial, CHECKS["preview"])
    permit = owner.begin("destroy-preview", NEW, previous=NEW)
    events = Events()
    record = {"calls": [], "objects": {"subs/effective.txt": b"fixture"}, "bucket": True, "mode": "normal"}
    store._write_mutex = threading.Lock()

    def contents():
        objects = dict(record["objects"])
        if store.body is not None:
            objects[STATE_KEY] = store.body
        for (operation, stage), body in store.stages.items():
            objects[f"_sba/operations/{operation}/{stage}.json"] = body
        return objects

    class Client:
        meta = SimpleNamespace(events=events)

        def list_objects_v2(self, *, Bucket, MaxKeys):
            assert Bucket == IDENTITY["bucket"] and MaxKeys == 1000
            return {"Contents": [{"Key": key, "ETag": '"' + hashlib.md5(body).hexdigest() + '"'}
                                 for key, body in sorted(contents().items())], "IsTruncated": False}

        def delete_object(self, *, Bucket, Key):
            assert Bucket == IDENTITY["bucket"]
            events.emit("before-send.s3.DeleteObject")
            record["calls"].append(("DeleteObject", Key))
            if record["mode"] == "object-unknown" and Key != STATE_KEY:
                raise OSError("fixture unknown")
            if Key == STATE_KEY:
                store.body = None
            elif Key.startswith("_sba/operations/"):
                _, _, operation, filename = Key.split("/")
                store.stages.pop((operation, filename.removesuffix(".json")))
            else:
                del record["objects"][Key]
            if record["mode"] == "control-response-lost" and Key == STATE_KEY:
                raise OSError("fixture unknown")
            if record["mode"] == "sdk-retry":
                events.emit("before-send.s3.DeleteObject")
                pytest.fail("guard must reject the second physical send")
            return {"ResponseMetadata": {"HTTPStatusCode": 204}}

        def delete_bucket(self, *, Bucket):
            events.emit("before-send.s3.DeleteBucket")
            assert Bucket == IDENTITY["bucket"] and contents() == {}
            record["calls"].append(("DeleteBucket", Bucket))
            record["bucket"] = False
            if record["mode"] == "bucket-response-lost":
                raise OSError("fixture unknown")
            return {"ResponseMetadata": {"HTTPStatusCode": 204}}

        def head_bucket(self, *, Bucket):
            record["calls"].append(("HeadBucket", Bucket))
            assert not record["bucket"]
            raise ClientError({"Error": {"Code": "NoSuchBucket"}, "ResponseMetadata": {"HTTPStatusCode": 404}}, "HeadBucket")

    store.client = Client()
    return owner, permit, record, events


def test_terminal_holds_control_until_all_other_objects_and_source_are_verified(terminal, tmp_path):
    owner, permit, record, events = terminal
    observations = []

    def source():
        assert state(owner.store)["writer"]["action"] == "destroy-preview"
        assert not record["objects"] and not owner.store.stages
        observations.append(True)
        return {"unchanged": True, "dataSha256": "a" * 64}

    path = tmp_path / "termination.safe.json"
    finish_bucket(owner, permit, path, source_unchanged=source, removed=[{"key": "resource-artifacts", "status": "removed"}])
    assert observations == [True] and owner.store.body is None and record["bucket"] is False
    assert record["calls"][-3:] == [("DeleteObject", STATE_KEY), ("DeleteBucket", IDENTITY["bucket"]), ("HeadBucket", IDENTITY["bucket"])]
    assert not events.handlers
    evidence = json.loads(path.read_bytes())
    assert evidence["bucketDeletion"] == "pending" and evidence["operationId"] == permit.operation_id
    assert len(owner.store.writes) == 3  # No early release/retired commit.


@pytest.mark.parametrize("mode", ["object-unknown", "sdk-retry", "control-response-lost", "bucket-response-lost"])
def test_unknown_delete_is_never_replayed_or_treated_as_definite_failure(terminal, tmp_path, mode):
    owner, permit, record, events = terminal
    record["mode"] = mode
    with pytest.raises((OSError, RefreshError)):
        finish_bucket(owner, permit, tmp_path / "termination.safe.json", source_unchanged=lambda: {"unchanged": True}, removed=[])
    assert not events.handlers
    assert len(record["calls"]) == len(set(record["calls"]))
    assert not any(call[0] == "HeadBucket" for call in record["calls"])
    if mode in ("object-unknown", "sdk-retry"):
        assert state(owner.store)["writer"]["action"] == "destroy-preview"
        assert not any(call[0] == "DeleteBucket" for call in record["calls"])
    elif mode == "control-response-lost":
        assert not any(call[0] == "DeleteBucket" for call in record["calls"])


def test_source_drift_prevents_control_or_bucket_deletion(terminal, tmp_path):
    owner, permit, record, _ = terminal
    with pytest.raises(RefreshError):
        finish_bucket(owner, permit, tmp_path / "termination.safe.json", source_unchanged=lambda: {"unchanged": False}, removed=[])
    assert state(owner.store)["writer"] is not None and record["bucket"] is True
    assert ("DeleteObject", STATE_KEY) not in record["calls"]
    assert not (tmp_path / "termination.safe.json").exists()


def test_terminal_preflight_enumerates_and_fsyncs_without_deleting_anything(terminal, tmp_path):
    owner, permit, record, _ = terminal
    path = tmp_path / "nested/termination.safe.json"
    preflight_bucket(owner, permit, path)
    assert record["calls"] == [] and record["bucket"] is True
    assert not path.exists()
    assert json.loads(path.with_name(path.name + ".preflight").read_bytes())["status"] == "preflight-only"


def test_terminal_preflight_rejects_known_bad_path_or_list_permission_before_deletes(terminal, tmp_path):
    owner, permit, record, _ = terminal
    path = tmp_path / "existing.json"
    path.write_bytes(b"preserved")
    with pytest.raises(RefreshError):
        preflight_bucket(owner, permit, path)
    assert path.read_bytes() == b"preserved" and record["calls"] == []

    def denied(**_):
        raise OSError("fixture permission denied")

    owner.store.client.list_objects_v2 = denied
    with pytest.raises(OSError):
        preflight_bucket(owner, permit, tmp_path / "output.json")
    assert record["calls"] == []
