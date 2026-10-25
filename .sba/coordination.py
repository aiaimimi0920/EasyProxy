"""套件升级与 Aggregator 刷新的共同持久栅栏；中断不自动释放、重试或夺锁。"""
from __future__ import annotations

from dataclasses import dataclass, field
import hashlib
import json
import re
import threading
from typing import Protocol
from urllib.parse import urlsplit
import uuid


STATE_KEY = "_sba/control-v1.json"
MAX_STATE_BYTES = 16384
SHA = re.compile(r"[a-f0-9]{40}\Z")
DIGEST = re.compile(r"[a-f0-9]{64}\Z")
NONCE = re.compile(r"[a-f0-9]{32}\Z")
VERSION = re.compile(r"(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\Z")
NAME = re.compile(r"[a-z][a-z0-9-]{1,62}\Z")
REPOSITORY = re.compile(r"[a-z0-9][a-z0-9_.-]{0,99}/[a-z0-9][a-z0-9_.-]{0,99}\Z")
ETAG = re.compile(r'"[a-fA-F0-9]{32}"\Z')
REQUIRED_CHECKS = {
    "deploy": {"suite-published", "service-ready"},
    "update": {"suite-published", "backup-created", "data-preserved", "service-ready"},
    "refresh": {"candidate-verified", "stable-verified", "misub-refreshed"},
    "preview": {"suite-published", "snapshot-copied", "migration-verified", "source-unchanged",
                "side-effects-isolated", "service-ready"},
    "destroy-preview": {"preview-removed", "source-unchanged"},
}


class CoordinationError(RuntimeError):
    """只提供安全错误码，绝不附加 provider 响应或原异常。"""


class StoreConflict(Exception):
    """原子条件不成立；这次写入被确定拒绝。"""


class StoreUnknown(Exception):
    """读取失败或写入结果未知；调用方不得自动重放。"""


class Store(Protocol):
    account_id: str
    bucket: str
    def read(self) -> tuple[bytes, str] | None: ...
    def is_empty(self) -> bool: ...
    def compare_and_set(self, body: bytes, etag: str | None) -> str: ...
    def create_stage(self, operation_id: str, stage: str, body: bytes) -> str: ...


def _require(condition: bool, category: str) -> None:
    if not condition:
        raise CoordinationError("EASYPROXY_SBA_COORDINATION_" + category)


def _matches(pattern: re.Pattern, value) -> bool:
    return isinstance(value, str) and pattern.fullmatch(value) is not None


def _keys(value, keys: set[str]) -> bool:
    return type(value) is dict and set(value) == keys


def _release(value) -> bool:
    return (_keys(value, {"sourceSha", "applicationVersion"}) and _matches(SHA, value["sourceSha"])
            and _matches(VERSION, value["applicationVersion"]))


def _identity(value) -> bool:
    return (_keys(value, {"repository", "environment", "accountId", "bucket", "purpose"})
            and _matches(REPOSITORY, value["repository"])
            and _matches(NAME, value["environment"]) and _matches(NONCE, value["accountId"])
            and _matches(NAME, value["bucket"]) and value["purpose"] in ("production", "preview"))


def _encode(value: dict | list) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("utf-8")


def inventory_digest(resources: list[dict]) -> str:
    """仅摘要批准资源的 kind/name/id；与版本、秘密和运行时 URL 无关。"""
    _require(type(resources) is list and 0 < len(resources) <= 32, "INVENTORY")
    _require(all(type(row) is dict and set(row) == {"kind", "name", "id"} and
                 all(isinstance(value, str) and 0 < len(value) <= 256 for value in row.values()) for row in resources),
             "INVENTORY")
    _require(len({(row["kind"], row["name"]) for row in resources}) == len(resources), "INVENTORY")
    raw = _encode(sorted(resources, key=lambda row: (row["kind"], row["name"])))
    return hashlib.sha256(raw).hexdigest()


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        _require(key not in result, "STATE")
        result[key] = value
    return result


def _decode(raw: bytes) -> dict:
    _require(type(raw) is bytes and 0 < len(raw) <= MAX_STATE_BYTES, "STATE")
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object)
    except (ValueError, UnicodeError, RecursionError):
        raise CoordinationError("EASYPROXY_SBA_COORDINATION_STATE") from None
    _require(_keys(value, {"schemaVersion", "identity", "inventoryDigest", "revision", "active", "writer", "retired"}), "STATE")
    _require(type(value["schemaVersion"]) is int and value["schemaVersion"] == 1, "STATE")
    _require(_identity(value["identity"]) and _matches(DIGEST, value["inventoryDigest"])
             and _matches(NONCE, value["revision"]) and type(value["retired"]) is bool, "STATE")
    _require(value["active"] is None or _release(value["active"]), "STATE")
    writer = value["writer"]
    if writer is not None:
        _require(_keys(writer, {"operationId", "action", "candidate"}) and _matches(NONCE, writer["operationId"])
                 and isinstance(writer["action"], str) and writer["action"] in REQUIRED_CHECKS
                 and _release(writer["candidate"]) and not value["retired"], "STATE")
        initial = writer["action"] in ("deploy", "preview")
        _require((value["active"] is None) == initial, "STATE")
        preview = value["identity"]["purpose"] == "preview"
        _require((writer["action"] != "preview" or preview) and (writer["action"] != "deploy" or not preview)
                 and (writer["action"] != "destroy-preview" or preview)
                 and (writer["action"] != "refresh" or not preview), "STATE")
        if writer["action"] in ("refresh", "destroy-preview"):
            _require(writer["candidate"] == value["active"], "STATE")
        if writer["action"] == "update":
            _require(_newer(writer["candidate"], value["active"]), "STATE")
    else:
        _require(value["active"] is not None, "STATE")
    _require(not value["retired"] or value["identity"]["purpose"] == "preview", "STATE")
    return value


def _newer(candidate: dict, previous: dict) -> bool:
    return (candidate["sourceSha"] != previous["sourceSha"] and
            tuple(map(int, candidate["applicationVersion"].split("."))) >
            tuple(map(int, previous["applicationVersion"].split("."))))


@dataclass(frozen=True)
class Permit:
    """只在当前进程持有；不是可跨任务重放的释放请求。"""
    operation_id: str
    action: str
    etag: str
    state_bytes: bytes
    _attempted_stages: set[str] = field(default_factory=set, repr=False, compare=False)
    _stage_mutex: object = field(default_factory=threading.Lock, repr=False, compare=False)


class Coordinator:
    def __init__(self, store: Store, identity: dict, inventory_digest: str):
        _require(_identity(identity) and _matches(DIGEST, inventory_digest), "IDENTITY")
        _require(store.account_id == identity["accountId"] and store.bucket == identity["bucket"], "IDENTITY")
        self.store = store
        self.identity = dict(identity)
        self.inventory_digest = inventory_digest

    def _read(self) -> tuple[dict, str] | None:
        try:
            result = self.store.read()
        except StoreUnknown:
            raise CoordinationError("EASYPROXY_SBA_COORDINATION_UNKNOWN") from None
        if result is None:
            return None
        raw, etag = result
        _require(_matches(ETAG, etag), "STATE")
        value = _decode(raw)
        _require(value["identity"] == self.identity and value["inventoryDigest"] == self.inventory_digest, "IDENTITY")
        return value, etag

    def current_release(self) -> dict:
        """仅选择已提交版本；忙碌或未知时不准备/运行周期发布。"""
        current = self._read()
        _require(current is not None, "VERSION")
        value, _ = current
        _require(not value["retired"], "RETIRED")
        _require(value["writer"] is None, "BUSY")
        return dict(value["active"])

    def _write(self, value: dict, etag: str | None) -> tuple[bytes, str]:
        raw = _encode(value)
        _decode(raw)
        try:
            new_etag = self.store.compare_and_set(raw, etag)
        except StoreConflict:
            raise CoordinationError("EASYPROXY_SBA_COORDINATION_CONFLICT") from None
        except StoreUnknown:
            raise CoordinationError("EASYPROXY_SBA_COORDINATION_UNKNOWN") from None
        _require(_matches(ETAG, new_etag), "UNKNOWN")
        return raw, new_etag

    def begin(self, action: str, candidate: dict, *, previous: dict | None = None) -> Permit:
        _require(isinstance(action, str) and action in REQUIRED_CHECKS and _release(candidate)
                 and (previous is None or _release(previous)), "VERSION")
        current = self._read()
        etag = None
        if current is None:
            _require(action in ("deploy", "preview") and previous is None, "VERSION")
            _require((action == "preview") == (self.identity["purpose"] == "preview"), "SIDE_EFFECTS")
            try:
                _require(self.store.is_empty(), "UNOWNED_BUCKET")
            except StoreUnknown:
                raise CoordinationError("EASYPROXY_SBA_COORDINATION_UNKNOWN") from None
            value = {"schemaVersion": 1, "identity": dict(self.identity), "inventoryDigest": self.inventory_digest,
                     "active": None, "retired": False}
        else:
            value, etag = current
            _require(not value["retired"], "RETIRED")
            _require(value["writer"] is None, "BUSY")
            _require(action not in ("deploy", "preview") and previous == value["active"], "VERSION")
            if action == "update":
                _require(_newer(candidate, previous), "VERSION")
            else:
                _require(candidate == previous, "VERSION")
            _require(action != "destroy-preview" or self.identity["purpose"] == "preview", "SIDE_EFFECTS")
            _require(action != "refresh" or self.identity["purpose"] == "production", "SIDE_EFFECTS")
        operation_id = uuid.uuid4().hex
        value["revision"] = uuid.uuid4().hex
        value["writer"] = {"operationId": operation_id, "action": action, "candidate": dict(candidate)}
        raw, written_etag = self._write(value, etag)
        return Permit(operation_id, action, written_etag, raw)

    def assert_owned(self, permit: Permit) -> dict:
        _require(type(permit) is Permit, "OWNERSHIP")
        current = self._read()
        _require(current is not None, "OWNERSHIP")
        value, etag = current
        writer = value["writer"]
        _require(etag == permit.etag and _encode(value) == permit.state_bytes and writer is not None
                 and writer["operationId"] == permit.operation_id and writer["action"] == permit.action, "OWNERSHIP")
        return value

    def commit_verified(self, permit: Permit, checks: dict[str, bool]) -> None:
        """调用方完成真实业务检查后提交；异常/取消路径绝不在 finally 中调用。"""
        _require(type(permit) is Permit and type(checks) is dict and
                 set(checks) == REQUIRED_CHECKS.get(permit.action) and all(value is True for value in checks.values()), "CHECKS")
        value = self.assert_owned(permit)
        if permit.action in ("deploy", "preview", "update"):
            value["active"] = dict(value["writer"]["candidate"])
        value["retired"] = permit.action == "destroy-preview"
        value["writer"] = None
        value["revision"] = uuid.uuid4().hex  # 每次空闲状态仍具有不同 ETag，避免 ABA。
        self._write(value, permit.etag)

    def claim_stage(self, permit: Permit, stage: str) -> None:
        """阶段级单次尝试；即使同一持有者误重入/并发，也不能再次发送业务写入。"""
        self.assert_owned(permit)
        _require(isinstance(stage, str) and re.fullmatch(r"[a-z][a-z-]{1,39}", stage) is not None, "STAGE")
        with permit._stage_mutex:
            _require(stage not in permit._attempted_stages, "STAGE_ALREADY_ATTEMPTED")
            permit._attempted_stages.add(stage)  # Even an UNKNOWN marker PUT cannot be replayed in this process.
        body = _encode({"operationId": permit.operation_id, "stage": stage,
                        "permitSha256": hashlib.sha256(permit.state_bytes).hexdigest()})
        try:
            etag = self.store.create_stage(permit.operation_id, stage, body)
        except StoreConflict:
            raise CoordinationError("EASYPROXY_SBA_COORDINATION_STAGE_ALREADY_ATTEMPTED") from None
        except StoreUnknown:
            raise CoordinationError("EASYPROXY_SBA_COORDINATION_UNKNOWN") from None
        _require(_matches(ETAG, etag), "UNKNOWN")
        self.assert_owned(permit)


class S3Store:
    """只访问未缓存的 S3 API；客户端必须禁止 SDK 自动重试。"""
    def __init__(self, client, account_id: str, bucket: str):
        _require(_matches(NONCE, account_id) and _matches(NAME, bucket), "IDENTITY")
        retries = client.meta.config.retries
        _require(type(retries) is dict and retries.get("total_max_attempts") == 1, "RETRIES")
        _require(client.meta.endpoint_url == f"https://{account_id}.r2.cloudflarestorage.com", "IDENTITY")
        self.client = client
        self.account_id = account_id
        self.bucket = bucket
        self._write_mutex = threading.Lock()
        client.meta.events.register_first("before-send.s3", self._same_endpoint)

    def _same_endpoint(self, request, **_kwargs):
        target = urlsplit(request.url)
        if target.scheme != "https" or target.netloc != f"{self.account_id}.r2.cloudflarestorage.com":
            raise StoreUnknown()

    def read(self) -> tuple[bytes, str] | None:
        try:
            response = self.client.get_object(Bucket=self.bucket, Key=STATE_KEY)
            stream = response["Body"]
            try:
                body = stream.read(MAX_STATE_BYTES + 1)
            finally:
                stream.close()
            return body, response.get("ETag")
        except Exception as exc:
            if _status(exc) == 404 and _code(exc) == "NoSuchKey":
                return None
            raise StoreUnknown() from None

    def is_empty(self) -> bool:
        try:
            response = self.client.list_objects_v2(Bucket=self.bucket, MaxKeys=1)
            return (type(response.get("KeyCount")) is int and response["KeyCount"] == 0
                    and response.get("Contents", []) == [] and response.get("IsTruncated") is False)
        except Exception:
            raise StoreUnknown() from None

    def compare_and_set(self, body: bytes, etag: str | None) -> str:
        _require(type(body) is bytes and 0 < len(body) <= MAX_STATE_BYTES
                 and (etag is None or _matches(ETAG, etag)), "STATE")
        condition = {"IfNoneMatch": "*"} if etag is None else {"IfMatch": etag}
        return self._put_once(STATE_KEY, body, condition)

    def create_stage(self, operation_id: str, stage: str, body: bytes) -> str:
        _require(_matches(NONCE, operation_id) and isinstance(stage, str)
                 and re.fullmatch(r"[a-z][a-z-]{1,39}", stage) is not None
                 and type(body) is bytes and 0 < len(body) <= MAX_STATE_BYTES, "STAGE")
        return self._put_once(f"_sba/operations/{operation_id}/{stage}.json", body, {"IfNoneMatch": "*"})

    def _put_once(self, key: str, body: bytes, condition: dict, *,
                  content_type: str = "application/json", cache_control: str = "no-store") -> str:
        sent = False

        def once(request, **_kwargs):
            nonlocal sent
            if sent:
                raise StoreUnknown()  # SDK 的区域重定向也不得绕过普通 retry 配置。
            sent = True

        with self._write_mutex:
            event, handler = "before-send.s3.PutObject", "easyproxy-sba-single-send"
            self.client.meta.events.register_first(event, once, unique_id=handler)
            try:
                response = self.client.put_object(Bucket=self.bucket, Key=key, Body=body,
                                                  ContentType=content_type, CacheControl=cache_control, **condition)
                return response.get("ETag")
            except Exception as exc:
                if _status(exc) == 412 and _code(exc) == "PreconditionFailed":
                    raise StoreConflict() from None
                raise StoreUnknown() from None
            finally:
                self.client.meta.events.unregister(event, unique_id=handler)


def _status(exc: Exception):
    response = getattr(exc, "response", {})
    return response.get("ResponseMetadata", {}).get("HTTPStatusCode") if isinstance(response, dict) else None


def _code(exc: Exception):
    response = getattr(exc, "response", {})
    return response.get("Error", {}).get("Code") if isinstance(response, dict) else None


def r2_store(account_id: str, bucket: str, access_key: str, secret_key: str) -> S3Store:
    """显式凭据、固定 TLS endpoint、短网络截止；不继承默认凭据链或会话 token。"""
    import boto3
    from botocore.config import Config

    _require(_matches(NONCE, account_id) and _matches(NAME, bucket)
             and all(isinstance(value, str) and 0 < len(value) <= 512 and all(33 <= ord(char) <= 126 for char in value)
                     for value in (access_key, secret_key)), "IDENTITY")
    client = boto3.client("s3", endpoint_url=f"https://{account_id}.r2.cloudflarestorage.com",
                          aws_access_key_id=access_key, aws_secret_access_key=secret_key, aws_session_token="",
                          region_name="auto", verify=True, config=Config(connect_timeout=10, read_timeout=30,
                          retries={"total_max_attempts": 1, "mode": "standard"},
                          s3={"addressing_style": "path"}))
    return S3Store(client, account_id, bucket)
