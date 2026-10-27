"""公开入口的规范身份；不以 URL 语法校验代替云端域名归属核验。"""
from __future__ import annotations

import re


LABEL = r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
HOST = re.compile(rf"(?:{LABEL}\.)+[a-z]{{2,63}}\Z")


def https_origin(value) -> bool:
    return (type(value) is str and value.startswith("https://") and len(value) <= 261
            and HOST.fullmatch(value[8:]) is not None)


def approved_origin(value, provider: str) -> bool:
    """Provider 身份必须准确相等；自定义入口仅接受规范的多标签主机名。"""
    return value == provider or (https_origin(value) and value[8:].count(".") >= 2
        and not value.endswith((".pages.dev", ".workers.dev")))


def inventory_origin(inventory: list[dict], service: str, provider: str) -> str:
    rows = [row for row in inventory if row.get("kind") == "domain" and row.get("name") == service]
    if not rows:
        return provider
    if len(rows) != 1 or not approved_origin(rows[0].get("id"), provider) or rows[0]["id"] == provider:
        raise ValueError("EASYPROXY_SBA_ORIGIN_REJECTED")
    return rows[0]["id"]
