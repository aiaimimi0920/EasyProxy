"""只保留有限阶段，不把异常正文、响应、配置或凭据放进回执。"""
from contextlib import contextmanager

from response_diagnostics import ResponseDiagnostic, RESPONSE_REASONS


PAGES_STAGES = frozenset({"PAGES_BUILD", "PAGES_PROJECT_READ", "PAGES_PARENT_VERIFY", "PAGES_CLAIM",
    "PAGES_PROJECT_CREATE", "PAGES_PROJECT_CONFIGURE", "PAGES_CONFIG_VERIFY", "PAGES_ASSET_TOKEN",
    "PAGES_ASSET_QUERY", "PAGES_ASSET_UPLOAD", "PAGES_ASSET_VERIFY", "PAGES_ASSET_REGISTER",
    "PAGES_PREDEPLOY_VERIFY", "PAGES_DEPLOYMENT_SUBMIT", "PAGES_DEPLOYMENT_READ", "PAGES_DEPLOYMENT_LIST",
    "PAGES_DEPLOYMENT_WAIT", "PAGES_DEPLOYMENT_VERIFY", "PAGES_PUBLIC_VERIFY", "PAGES_FINAL_OWNER"})


STAGES = {"SOURCE", "PYTHON", "CREDENTIALS", "OUTPUT", "TOOLS", "RUNTIME", "IMAGE", "SEALER", "GENERATION",
          "PRISTINE", "MIGRATION", "PUBLISH", "VERIFY", "DESTROY_PREVIEW", "WRITER", "D1", "GITHUB",
          "EDGE", "PAGES", "DOMAINS", "CANDIDATE", "AUDIT", "PROMOTION", "SYNC", "COMMIT",
          "GENERATION_SOURCE", "GENERATION_RUNTIME", "GENERATION_CONFIG", "GENERATION_COLLECT",
          "GENERATION_READBACK", "GENERATION_OUTPUTS", "GENERATION_CONVERSION", "GENERATION_VALIDATION"} | PAGES_STAGES


class StageError(RuntimeError):
    def __init__(self, stage, reason=None):
        if type(stage) is not str or stage not in STAGES or (reason is not None and
                (stage not in PAGES_STAGES or type(reason) is not str or reason not in RESPONSE_REASONS)):
            raise ValueError("EASYPROXY_SBA_DIAGNOSTIC_REJECTED")
        self.stage, self.reason = stage, reason
        super().__init__("EASYPROXY_SBA_" + stage + ("_" + reason if reason is not None else "") + "_UNKNOWN")


def safe_error_code(error: Exception) -> str:
    if type(error) is StageError and type(error.stage) is str and error.stage in STAGES and (error.reason is None or
            error.stage in PAGES_STAGES and type(error.reason) is str and error.reason in RESPONSE_REASONS):
        return "EASYPROXY_SBA_" + error.stage + ("_" + error.reason if error.reason is not None else "") + "_UNKNOWN"
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


@contextmanager
def pages_phase(stage):
    """只给固定Pages步骤附加有限HTTP分类；不改变请求、重试或成功判断。"""
    if type(stage) is not str or stage not in PAGES_STAGES:
        raise ValueError("EASYPROXY_SBA_DIAGNOSTIC_REJECTED")
    diagnostic = ResponseDiagnostic()
    try:
        yield diagnostic
    except StageError:
        raise
    except Exception:
        reason = diagnostic.reason
        if type(reason) is not str or reason not in RESPONSE_REASONS:
            reason = None
        raise StageError(stage, reason) from None
