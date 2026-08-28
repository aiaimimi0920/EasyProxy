#!/usr/bin/env python3
"""Create and verify the native GitHub Release manifest and checksums."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path


REQUIRED = (
    "easyproxy-linux-amd64.tar.gz",
    "easyproxy-linux-arm64.tar.gz",
    "easyproxy-windows-amd64.zip",
    "easyproxyctl-linux-amd64.tar.gz",
    "easyproxyctl-linux-arm64.tar.gz",
    "easyproxyctl-windows-amd64.zip",
    "config.example.yaml",
    "easyproxy.service",
    "install.sh",
    "install-service.ps1",
    "native-install-update.md",
)
CHECKSUM_RE = re.compile(r"^([0-9a-f]{64})  ([A-Za-z0-9._-]+)$")


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def build(directory: Path, version: str, commit: str) -> None:
    missing = [name for name in REQUIRED if not (directory / name).is_file()]
    if missing:
        raise SystemExit(f"missing native release artifacts: {', '.join(missing)}")
    artifacts = [
        {"name": name, "sha256": digest(directory / name), "size": (directory / name).stat().st_size}
        for name in REQUIRED
    ]
    manifest = {
        "schemaVersion": 1,
        "project": "EasyProxy",
        "version": version,
        "commit": commit,
        "supportedTargets": ["linux-amd64", "linux-arm64", "windows-amd64"],
        "unsupportedTargets": [{"target": "windows-arm64", "reason": "No supported native service artifact"}],
        "artifacts": artifacts,
    }
    manifest_path = directory / "release-manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    checksummed = [*REQUIRED, manifest_path.name]
    lines = [f"{digest(directory / name)}  {name}" for name in sorted(checksummed)]
    (directory / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="utf-8")


def verify(directory: Path, expected_version: str | None = None, expected_commit: str | None = None) -> None:
    manifest_path = directory / "release-manifest.json"
    sums_path = directory / "SHA256SUMS"
    if not manifest_path.is_file() or not sums_path.is_file():
        raise SystemExit("release-manifest.json and SHA256SUMS are required")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("schemaVersion") != 1 or manifest.get("project") != "EasyProxy":
        raise SystemExit("native release manifest identity mismatch")
    if not isinstance(manifest.get("version"), str) or not manifest["version"]:
        raise SystemExit("native release manifest version is required")
    if not isinstance(manifest.get("commit"), str) or not manifest["commit"]:
        raise SystemExit("native release manifest commit is required")
    if expected_version and manifest["version"] != expected_version:
        raise SystemExit("native release manifest version mismatch")
    if expected_commit and manifest["commit"] != expected_commit:
        raise SystemExit("native release manifest commit mismatch")
    if manifest.get("supportedTargets") != ["linux-amd64", "linux-arm64", "windows-amd64"]:
        raise SystemExit("native release target contract mismatch")
    if manifest.get("unsupportedTargets") != [{"target": "windows-arm64", "reason": "No supported native service artifact"}]:
        raise SystemExit("Windows arm64 must be explicitly unsupported")
    expected = {}
    for line in sums_path.read_text(encoding="utf-8").splitlines():
        match = CHECKSUM_RE.fullmatch(line)
        if not match:
            raise SystemExit(f"invalid SHA256SUMS line: {line}")
        checksum, name = match.groups()
        if name in expected:
            raise SystemExit(f"duplicate SHA256SUMS entry: {name}")
        expected[name] = checksum
    required_checksums = {*REQUIRED, "release-manifest.json"}
    if set(expected) != required_checksums:
        raise SystemExit("SHA256SUMS artifact set mismatch")
    for name, checksum in expected.items():
        path = directory / name
        if not path.is_file() or digest(path) != checksum:
            raise SystemExit(f"checksum mismatch: {name}")
    artifacts = manifest.get("artifacts")
    if not isinstance(artifacts, list) or any(not isinstance(item, dict) for item in artifacts):
        raise SystemExit("release manifest artifacts must be objects")
    artifact_names = [item.get("name") for item in artifacts]
    if len(artifact_names) != len(set(artifact_names)) or set(artifact_names) != set(REQUIRED):
        raise SystemExit("release manifest artifact set mismatch")
    for item in artifacts:
        path = directory / item["name"]
        if not re.fullmatch(r"[0-9a-f]{64}", str(item.get("sha256", ""))):
            raise SystemExit(f"invalid manifest checksum: {item['name']}")
        if digest(path) != item["sha256"] or path.stat().st_size != item.get("size"):
            raise SystemExit(f"manifest mismatch: {item['name']}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--version")
    parser.add_argument("--commit")
    parser.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    if args.verify:
        verify(args.directory, args.version, args.commit)
    else:
        if not args.version or not args.commit:
            parser.error("--version and --commit are required when building")
        build(args.directory, args.version, args.commit)


if __name__ == "__main__":
    main()
