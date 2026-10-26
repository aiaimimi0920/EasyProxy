"""仅允许对首个已发布版本的通用 UNKNOWN 声明全资源 pristine 修复。"""
REPAIR_ID = "pristine-initial-deployment"
PARENT_RELEASE = {"sourceSha": "1fc843033ba5f39384772d1dccb4ccd90deda03d", "applicationVersion": "1.0.0"}
PARENT_ERROR = "EASYPROXY_SBA_FAILED_OR_UNKNOWN"
CONTEXT_FIELDS = {"repairId", "parentTaskId", "parentRunId", "requestDigest", "resultDigest", "errorCode"}

# 此操作只恢复已核实的这一份失败回执，不是通用的部分发布接管入口。
GITHUB_REPAIR_ID = "continue-github-initial-deployment"
GITHUB_PARENT_RELEASE = {"sourceSha": "9f1c66030fb2913f43401da8cf6328ef57608bf3", "applicationVersion": "1.0.1"}
GITHUB_CONTEXT = {
    "repairId": GITHUB_REPAIR_ID,
    "parentTaskId": "dc-d5f5ecfba7e94f49b9f088dfed7ff496",
    "parentRunId": 40272310671,
    "requestDigest": "60a7f6dcdb72f083a279e156b85357bdf14a474f5d4e3b070a80ca90d0f56372",
    "resultDigest": "b8aa25d210dbc5f2feb3df0c4d3a1f1dfad9f80b998603b7033e233becbfc03a",
    "errorCode": "EASYPROXY_SBA_GITHUB_UNKNOWN",
}
