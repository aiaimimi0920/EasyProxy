"""只保留有限阶段，不把异常正文、响应、配置或凭据放进回执。"""
from contextlib import contextmanager
import re

from response_diagnostics import ResponseDiagnostic, RESPONSE_REASONS, valid_provider_code, valid_provider_status


PAGES_STAGES = frozenset({"PAGES_BUILD", "PAGES_PROJECT_READ", "PAGES_PARENT_VERIFY", "PAGES_CLAIM",
    "PAGES_PROJECT_CREATE", "PAGES_PROJECT_CONFIGURE", "PAGES_CONFIG_VERIFY", "PAGES_ASSET_TOKEN",
    "PAGES_ASSET_QUERY", "PAGES_ASSET_UPLOAD", "PAGES_ASSET_VERIFY", "PAGES_ASSET_REGISTER",
    "PAGES_PREDEPLOY_VERIFY", "PAGES_DEPLOYMENT_SUBMIT", "PAGES_DEPLOYMENT_READ", "PAGES_DEPLOYMENT_LIST",
    "PAGES_DEPLOYMENT_WAIT", "PAGES_DEPLOYMENT_VERIFY", "PAGES_PUBLIC_VERIFY", "PAGES_FINAL_OWNER"})


STAGES = {"SOURCE", "PYTHON", "CREDENTIALS", "OUTPUT", "TOOLS", "RUNTIME", "IMAGE", "SEALER", "GENERATION",
          "PRISTINE", "FRESH_PRISTINE", "MIGRATION", "PUBLISH", "VERIFY", "DESTROY_PREVIEW", "WRITER", "D1", "GITHUB",
          "EDGE", "PAGES", "DOMAINS", "CANDIDATE", "AUDIT", "PROMOTION", "SYNC", "COMMIT", "REFRESH_ABORT",
          "GENERATION_SOURCE", "GENERATION_RUNTIME", "GENERATION_CONFIG", "GENERATION_COLLECT",
          "GENERATION_READBACK", "GENERATION_OUTPUTS", "GENERATION_CONVERSION", "GENERATION_VALIDATION"} | PAGES_STAGES

# 只识别本地 ArtifactError 的完整固定码；不截取/回显任意异常正文。
PROMOTION_REASONS = frozenset({"IDENTITY", "AUDIT", "AUDIT_IMAGE", "AUDIT_CLEANUP", "INPUT", "EMPTY", "FORMAT",
    "COUNTS", "COUNT_DROP", "STABLE_MISSING", "UNOWNED_STABLE", "STABLE_IDENTITY", "STABLE_MANIFEST",
    "STABLE_DRIFT", "CANDIDATE_DRIFT", "SNAPSHOT_DRIFT", "PUBLIC_UNKNOWN", "PUBLIC_MISMATCH",
    "READ_UNKNOWN", "CONFLICT", "WRITE_UNKNOWN", "READBACK_UNKNOWN", "COORDINATION_UNKNOWN",
    "COORDINATION_OWNERSHIP", "COORDINATION_STAGE_ALREADY_ATTEMPTED", "COORDINATION_STAGE_UNKNOWN"})
STAGE_REASONS = {**dict.fromkeys(PAGES_STAGES, RESPONSE_REASONS), "PROMOTION": PROMOTION_REASONS}


def valid_reason(stage, reason):
    return reason is None or type(reason) is str and reason in STAGE_REASONS.get(stage, ())


class StageError(RuntimeError):
    def __init__(self, stage, reason=None, *, provider_status=None, provider_code=None):
        if (type(stage) is not str or stage not in STAGES or not valid_reason(stage, reason)
                or (provider_status is not None and not valid_provider_status(provider_status))
                or (provider_code is not None and not valid_provider_code(provider_code))):
            raise ValueError("EASYPROXY_SBA_DIAGNOSTIC_REJECTED")
        self.stage, self.reason = stage, reason
        self.provider_status, self.provider_code = provider_status, provider_code
        super().__init__("EASYPROXY_SBA_" + stage + ("_" + reason if reason is not None else "") + "_UNKNOWN")


def safe_error_code(error: Exception) -> str:
    if type(error) is StageError and type(error.stage) is str and error.stage in STAGES and valid_reason(error.stage, error.reason):
        return "EASYPROXY_SBA_" + error.stage + ("_" + error.reason if error.reason is not None else "") + "_UNKNOWN"
    return "EASYPROXY_SBA_FAILED_OR_UNKNOWN"


def result_diagnostics(error: Exception | None = None, *, failed_step: str | None = None,
                       duration_ms: int | None = None) -> dict | None:
    value = {}
    if type(error) is StageError and type(error.stage) is str and error.stage in STAGES and valid_reason(error.stage, error.reason):
        value["failedStep"] = error.stage.lower().replace("_", "-")
        status = getattr(error, "provider_status", None)
        if valid_provider_status(status):
            value["providerStatus"] = status
        code = getattr(error, "provider_code", None)
        if valid_provider_code(code):
            value["providerCode"] = code
        elif error.reason is not None and valid_provider_code(error.reason):
            value["providerCode"] = error.reason
    elif failed_step is not None and type(failed_step) is str and re.fullmatch(r"[a-z][a-z0-9-]{1,62}", failed_step) is not None:
        value["failedStep"] = failed_step
    if type(duration_ms) is int:
        value["durationMs"] = min(3600000, max(0, duration_ms))
    return value or None


@contextmanager
def phase(stage: str):
    if stage not in STAGES:
        raise ValueError("EASYPROXY_SBA_DIAGNOSTIC_REJECTED")
    try:
        yield
    except StageError:
        raise
    except Exception as error:
        reason = None
        if stage == "PROMOTION":
            from artifact_store import ArtifactError
            from coordination import CoordinationError
            if type(error) is ArtifactError:
                reason = next((item for item in PROMOTION_REASONS
                    if error.args == ("EASYPROXY_SBA_ARTIFACT_" + item,)), None)
            elif type(error) is CoordinationError:
                reason = next((item for item in PROMOTION_REASONS if item.startswith("COORDINATION_")
                    and error.args == ("EASYPROXY_SBA_" + item,)), None)
        raise StageError(stage, reason) from None


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
        reason = diagnostic.reason if type(diagnostic.reason) is str and diagnostic.reason in RESPONSE_REASONS else None
        status = diagnostic.provider_status if valid_provider_status(diagnostic.provider_status) else None
        code = diagnostic.provider_code if valid_provider_code(diagnostic.provider_code) else None
        raise StageError(stage, reason, provider_status=status, provider_code=code) from None
