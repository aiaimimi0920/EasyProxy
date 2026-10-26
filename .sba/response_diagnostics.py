"""固定HTTP分类载体，不持有请求、响应、凭据或异常正文。"""

RESPONSE_REASONS = frozenset({"HTTP_3XX", "HTTP_400", "HTTP_401", "HTTP_403", "HTTP_404", "HTTP_409",
    "HTTP_413", "HTTP_429", "HTTP_4XX", "HTTP_5XX", "HTTP_OTHER", "SIZE", "DECODE", "ENVELOPE",
    "TIMEOUT", "NETWORK", "REQUEST"})


class ResponseDiagnostic:
    __slots__ = ("reason",)

    def __init__(self):
        self.reason = None
