"""只保留有限阶段，不把异常正文、响应、配置或凭据放进回执。"""
from contextlib import contextmanager


STAGES = {"SOURCE", "PYTHON", "CREDENTIALS", "OUTPUT", "TOOLS", "RUNTIME", "IMAGE", "SEALER", "GENERATION",
          "PRISTINE", "MIGRATION", "PUBLISH", "VERIFY", "DESTROY_PREVIEW", "WRITER", "D1", "GITHUB",
          "EDGE", "PAGES", "CANDIDATE", "AUDIT", "PROMOTION", "SYNC", "COMMIT"}


class StageError(RuntimeError):
    def __init__(self, stage):
        if stage not in STAGES:
            raise ValueError("EASYPROXY_SBA_DIAGNOSTIC_REJECTED")
        self.stage = stage
        super().__init__("EASYPROXY_SBA_" + stage + "_UNKNOWN")


def safe_error_code(error: Exception) -> str:
    if type(error) is StageError and error.stage in STAGES:
        return "EASYPROXY_SBA_" + error.stage + "_UNKNOWN"
    return "EASYPROXY_SBA_FAILED_OR_UNKNOWN"


@contextmanager
def phase(stage: str):
    if stage not in STAGES:
        raise ValueError("EASYPROXY_SBA_DIAGNOSTIC_REJECTED")
    try:
        yield
    except StageError:
        raise
    except Exception:
        raise StageError(stage) from None
