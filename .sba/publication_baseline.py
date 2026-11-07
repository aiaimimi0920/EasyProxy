"""已核验的部分发布基线；只服务准确恢复 permit，不替换 active/stable。"""
from dataclasses import dataclass
import hashlib

from coordination import Coordinator, Permit
from repair_context import MIGRATION_ACTIVE_RELEASE, PROMOTION_PARENT_RELEASE


@dataclass(frozen=True)
class PublicationBaseline:
    operation_id: str
    permit_sha256: str

    @property
    def release(self):
        return dict(PROMOTION_PARENT_RELEASE)

    def assert_permit(self, owner: Coordinator, permit: Permit):
        state = owner.assert_owned(permit)
        if not (permit.action == 'update' and owner.identity['purpose'] == 'production'
                and state['active'] == MIGRATION_ACTIVE_RELEASE
                and state['writer']['candidate']['applicationVersion'] == '1.0.17'
                and permit.operation_id == self.operation_id
                and hashlib.sha256(permit.state_bytes).hexdigest() == self.permit_sha256):
            raise RuntimeError('EASYPROXY_SBA_PUBLICATION_BASELINE_REJECTED')
