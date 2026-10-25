"""可选严格审计镜像身份；不 build、不 pull、不根据可变标签选择运行对象。"""
from __future__ import annotations

import json
import re
import subprocess


def inspect_image(image_id: str, source_sha: str) -> dict:
    if re.fullmatch(r"sha256:[a-f0-9]{64}", image_id) is None or re.fullmatch(r"[a-f0-9]{40}", source_sha) is None:
        raise ValueError("EASYPROXY_AUDIT_IMAGE_IDENTITY")
    result = subprocess.run(["docker", "image", "inspect", image_id], capture_output=True, text=True,
                            stdin=subprocess.DEVNULL, timeout=30, check=False)
    if result.returncode != 0 or len(result.stdout) > 256 * 1024:
        raise RuntimeError("EASYPROXY_AUDIT_IMAGE_IDENTITY")
    try:
        rows = json.loads(result.stdout)
        if type(rows) is not list or len(rows) != 1:
            raise ValueError("shape")
        row = rows[0]
        labels = row.get("Config", {}).get("Labels", {})
        if (row.get("Id") != image_id or row.get("Os") != "linux" or row.get("Architecture") != "amd64"
                or labels.get("org.opencontainers.image.revision") != source_sha
                or labels.get("easyproxy.audit.scope") != "subscription-only-v1"):
            raise ValueError("identity")
    except (ValueError, TypeError, AttributeError):
        raise RuntimeError("EASYPROXY_AUDIT_IMAGE_IDENTITY") from None
    return {"imageId": image_id, "sourceSha": source_sha, "platform": "linux/amd64", "scope": "subscription-only-v1"}


def verify_container(container: str, image_id: str) -> None:
    result = subprocess.run(["docker", "inspect", container, "--format", "{{.Image}}"], capture_output=True,
                            text=True, stdin=subprocess.DEVNULL, timeout=30, check=False)
    if result.returncode != 0 or result.stdout.strip() != image_id:
        raise RuntimeError("EASYPROXY_AUDIT_CONTAINER_IMAGE_MISMATCH")
