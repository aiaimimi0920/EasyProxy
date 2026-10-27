"""只属于准确新实例的进程内许可；不扩大历史 PagesResume 的资源范围。"""
from dataclasses import dataclass
import hashlib
import re

from coordination import Coordinator, Permit

WORKER_BUNDLE_SHA256 = "0216cff123ad16fc7790f7338cc4fe9479af64d7810029b47fe31dd03c5f5704"


class FreshDomainError(RuntimeError):
    pass


def require(value):
    if not value:
        raise FreshDomainError("EASYPROXY_SBA_FRESH_DOMAIN_REJECTED") from None


@dataclass(frozen=True)
class FreshDomainResume:
    operation_id: str
    permit_sha256: str
    workers_subdomain: str

    def assert_permit(self, owner: Coordinator, permit: Permit):
        state = owner.assert_owned(permit)
        require(permit.action == "deploy" and owner.identity["purpose"] == "production"
                and type(self.workers_subdomain) is str and re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", self.workers_subdomain)
                and permit.operation_id == self.operation_id
                and hashlib.sha256(permit.state_bytes).hexdigest() == self.permit_sha256
                and state["writer"]["candidate"]["applicationVersion"] == "1.0.13")
