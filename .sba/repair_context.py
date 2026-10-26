"""仅允许对首个已发布版本的通用 UNKNOWN 声明全资源 pristine 修复。"""
REPAIR_ID = "pristine-initial-deployment"
PARENT_RELEASE = {"sourceSha": "1fc843033ba5f39384772d1dccb4ccd90deda03d", "applicationVersion": "1.0.0"}
PARENT_ERROR = "EASYPROXY_SBA_FAILED_OR_UNKNOWN"
CONTEXT_FIELDS = {"repairId", "parentTaskId", "parentRunId", "requestDigest", "resultDigest", "errorCode"}
