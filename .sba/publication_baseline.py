"""已核验的部分发布基线；只服务准确恢复 permit，不替换 active/stable。"""
from dataclasses import dataclass
import hashlib

from coordination import Coordinator, Permit
from repair_context import (
    AUDIT_TIMEOUT_ACTIVE_RELEASE,
    AUDIT_TIMEOUT_PARENT_RELEASE,
    MIGRATION_ACTIVE_RELEASE,
    PROMOTION_COPY_PARENT_RELEASE,
    PROMOTION_PARENT_RELEASE,
)


@dataclass(frozen=True)
class PublicationBaseline:
    operation_id: str
    permit_sha256: str
    recovery: str = 'edge16'

    @property
    def release(self):
        if self.recovery == 'edge16':
            return dict(PROMOTION_PARENT_RELEASE)
        if self.recovery == 'first-release17':
            return dict(PROMOTION_COPY_PARENT_RELEASE)
        if self.recovery == 'audit26':
            return dict(AUDIT_TIMEOUT_PARENT_RELEASE)
        raise RuntimeError('EASYPROXY_SBA_PUBLICATION_BASELINE_REJECTED')

    def assert_permit(self, owner: Coordinator, permit: Permit):
        state = owner.assert_owned(permit)
        expected_active = AUDIT_TIMEOUT_ACTIVE_RELEASE if self.recovery == 'audit26' else MIGRATION_ACTIVE_RELEASE
        if not (permit.action == 'update' and owner.identity['purpose'] == 'production'
                and state['active'] == expected_active
                and (self.recovery, state['writer']['candidate']['applicationVersion'])
                    in (('edge16', '1.0.17'), ('first-release17', '1.0.18'), ('audit26', '1.0.27'))
                and permit.operation_id == self.operation_id
                and hashlib.sha256(permit.state_bytes).hexdigest() == self.permit_sha256):
            raise RuntimeError('EASYPROXY_SBA_PUBLICATION_BASELINE_REJECTED')
