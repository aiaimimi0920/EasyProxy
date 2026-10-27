"""准确新实例的公开配置绑定；不公开账号，只接受批准配置的完整摘要。"""
import hashlib
import json

from suite_config import SuiteConfig

CONFIGURATION_SHA256 = "7dae01c58ecd80eaa34df6f3e2f5dbbb8e32ac3227f464ce348123dc709776a1"


def configuration_matches(config):
    if type(config) is not SuiteConfig:
        return False
    value = {"environment": config.environment, "configuration": config.configuration()}
    digest = hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True,
                                      separators=(",", ":")).encode("utf-8")).hexdigest()
    return config.repository == "aiaimimi0920/easyproxy" and digest == CONFIGURATION_SHA256
