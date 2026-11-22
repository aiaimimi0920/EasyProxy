"""固定HTTP分类载体，不持有请求、响应、凭据或异常正文。"""
import re

RESPONSE_REASONS = frozenset({"HTTP_3XX", "HTTP_400", "HTTP_401", "HTTP_403", "HTTP_404", "HTTP_409",
    "HTTP_413", "HTTP_429", "HTTP_4XX", "HTTP_5XX", "HTTP_OTHER", "SIZE", "DECODE", "ENVELOPE",
    "TIMEOUT", "NETWORK", "REQUEST"})


def valid_provider_status(value) -> bool:
    return type(value) is int and 100 <= value <= 599


def valid_provider_code(value) -> bool:
    return ((type(value) is int and 0 <= value <= 999999999)
            or (type(value) is str and re.fullmatch(r"[A-Z][A-Z0-9_]{1,63}", value) is not None))


class ResponseDiagnostic:
    __slots__ = ("reason", "provider_status", "provider_code")

    def __init__(self):
        self.reason = None
        self.provider_status = None
        self.provider_code = None

    def reset(self):
        self.reason = None
        self.provider_status = None
        self.provider_code = None
