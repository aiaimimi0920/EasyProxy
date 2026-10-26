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

PAGES_REPAIR_ID = "continue-pages-initial-deployment"
PAGES_PARENT_RELEASE = {"sourceSha": "cdc04e167e951850c00cb8b1fe0c44878c4a118e", "applicationVersion": "1.0.2"}
PAGES_CONTEXT = {
    "repairId": PAGES_REPAIR_ID,
    "parentTaskId": "dc-c982bc121b8548d78474848a8065b296",
    "parentRunId": 40280908267,
    "requestDigest": "8165c2e348839267a8693e8a06f1d110e53be2047406be9d6aec3ec32f74345a",
    "resultDigest": "0e6d8cc8b2e6dd1a15f64ddcfffc308a2a9d5b753cd8f8ea273a7b33495265a1",
    "errorCode": "EASYPROXY_SBA_PAGES_UNKNOWN",
}

ASSETS_REPAIR_ID = "continue-pages-assets-deployment"
ASSETS_PARENT_RELEASE = {"sourceSha": "097ef164bd8980e880cb8044f24f5bad4be1ef69", "applicationVersion": "1.0.3"}
ASSETS_CONTEXT = {
    "repairId": ASSETS_REPAIR_ID,
    "parentTaskId": "dc-3dad4093eba94806b436848210a958fb",
    "parentRunId": 40288989518,
    "requestDigest": "7b1360af73689f933536b3e9297ce073cf1cd202a1e90d2842d2c461a566a38d",
    "resultDigest": "7ccfdffe0f7a0b5e3d19283c992e1788a1fd4e43810efac3411c70b5fe734170",
    "errorCode": "EASYPROXY_SBA_PAGES_UNKNOWN",
}
