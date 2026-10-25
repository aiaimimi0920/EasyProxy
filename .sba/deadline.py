"""Linux 动作总预算；预留三分钟让审计清理与安全回执结束。"""
from contextlib import contextmanager
import math
import signal
import threading
import time


MAX_SECONDS = 3300


class ExecutionDeadline(TimeoutError):
    def __init__(self):
        super().__init__("EASYPROXY_SBA_DEADLINE")


def remaining(deadline: float) -> float:
    value = deadline - time.monotonic()
    if not math.isfinite(value) or not 0 < value <= MAX_SECONDS:
        raise ExecutionDeadline()
    return value


@contextmanager
def action_deadline(deadline: float):
    if threading.current_thread() is not threading.main_thread() or not hasattr(signal, "setitimer"):
        raise ExecutionDeadline()
    seconds = remaining(deadline)
    if signal.getitimer(signal.ITIMER_REAL) != (0.0, 0.0):
        raise ExecutionDeadline()

    def expired(*_):
        raise ExecutionDeadline()

    previous = signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, seconds)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous)
