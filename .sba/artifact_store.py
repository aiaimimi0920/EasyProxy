"""Aggregator 有界对象读写；复用共同 S3 单发送保护，不执行删除或恢复。"""
from __future__ import annotations

from dataclasses import dataclass
import re

from coordination import ETAG, S3Store, StoreConflict, StoreUnknown, _code, _status


MAX_BYTES = 64 * 1024 * 1024
KEY = re.compile(r"[A-Za-z0-9][A-Za-z0-9._/-]{0,511}\Z")


class ArtifactError(RuntimeError):
    """只暴露安全错误码，不附加订阅或 provider 正文。"""


def require(condition: bool, code: str) -> None:
    if not condition:
        raise ArtifactError("EASYPROXY_SBA_ARTIFACT_" + code)


@dataclass(frozen=True)
class Object:
    body: bytes
    etag: str


class ArtifactStore:
    def __init__(self, store: S3Store):
        require(isinstance(store, S3Store), "IDENTITY")
        self.store = store

    @staticmethod
    def _key(key: str) -> None:
        require(isinstance(key, str) and KEY.fullmatch(key) is not None
                and all(part not in ("", ".", "..") for part in key.split("/"))
                and not key.startswith("_sba/"), "KEY")

    def get(self, key: str) -> Object | None:
        self._key(key)
        try:
            response = self.store.client.get_object(Bucket=self.store.bucket, Key=key)
            stream = response["Body"]
            try:
                body = stream.read(MAX_BYTES + 1)
            finally:
                stream.close()
            etag = response.get("ETag")
            require(type(body) is bytes and len(body) <= MAX_BYTES
                    and isinstance(etag, str) and ETAG.fullmatch(etag) is not None, "READ_UNKNOWN")
            return Object(body, etag)
        except ArtifactError:
            raise
        except Exception as exc:
            if _status(exc) == 404 and _code(exc) == "NoSuchKey":
                return None
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_READ_UNKNOWN") from None

    def put(self, key: str, body: bytes, previous: Object | None, *, immutable: bool = False) -> Object:
        self._key(key)
        require(type(body) is bytes and 0 < len(body) <= MAX_BYTES
                and (previous is None or type(previous) is Object)
                and (not immutable or previous is None), "INPUT")
        content_type = ("application/json; charset=utf-8" if key.endswith(".json") else
                        "application/yaml; charset=utf-8" if key.endswith(".yaml") else "text/plain; charset=utf-8")
        condition = {"IfNoneMatch": "*"} if previous is None else {"IfMatch": previous.etag}
        try:
            etag = self.store._put_once(key, body, condition, content_type=content_type,
                                       cache_control="public, max-age=31536000, immutable" if immutable else "no-store")
        except StoreConflict:
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_CONFLICT") from None
        except StoreUnknown:
            raise ArtifactError("EASYPROXY_SBA_ARTIFACT_WRITE_UNKNOWN") from None
        require(isinstance(etag, str) and ETAG.fullmatch(etag) is not None, "WRITE_UNKNOWN")
        actual = self.get(key)
        require(actual is not None and actual.body == body and actual.etag == etag, "READBACK_UNKNOWN")
        return actual
