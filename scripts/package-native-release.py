#!/usr/bin/env python3
"""Build deterministic native EasyProxy and easyproxyctl release archives."""

from __future__ import annotations

import argparse
import gzip
import shutil
import tarfile
import tempfile
import zipfile
from pathlib import Path


ROOT = Path(__file__).parents[1]
FIXED_TIME = (1980, 1, 1, 0, 0, 0)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--platform", choices=("linux", "windows"), required=True)
    parser.add_argument("--arch", choices=("amd64", "arm64"), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--easy-proxy", type=Path, required=True)
    parser.add_argument("--easyproxyctl", type=Path, required=True)
    parser.add_argument("--ech-workers", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    return parser.parse_args()


def render_config(platform: str, version: str) -> str:
    text = (ROOT / "deploy/service/base/config.template.yaml").read_text(encoding="utf-8")
    if platform == "linux":
        text = text.replace("/usr/local/bin/ech-workers", "/opt/easyproxy/current/bin/ech-workers")
        text = text.replace("/usr/local/bin/cfst", "/opt/easyproxy/current/bin/cfst")
        text = text.replace("/usr/share/easyproxy/cfst", "/opt/easyproxy/current/share/cfst")
    else:
        text = text.replace("/var/lib/easyproxy/data/data.db", "C:/ProgramData/EasyProxy/data/data.db")
        text = text.replace("/var/lib/easyproxy/GeoLite2-Country.mmdb", "C:/ProgramData/EasyProxy/GeoLite2-Country.mmdb")
        text = text.replace("/var/lib/easyproxy/connectors", "C:/ProgramData/EasyProxy/connectors")
        text = text.replace('connector_runtime:\n    enabled: true', 'connector_runtime:\n    enabled: false')
    text = text.replace("__EASYPROXY_RELEASE_VERSION__", version)
    return text


def copy_binary(source: Path, destination: Path) -> None:
    if not source.is_file():
        raise SystemExit(f"missing release binary: {source}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)
    destination.chmod(0o755)


def normalize_tree(root: Path) -> None:
    for path in sorted(root.rglob("*")):
        path.chmod(0o755 if path.is_dir() or path.parent.name == "bin" or path.suffix in {".sh", ".ps1"} else 0o644)


def make_zip(source: Path, destination: Path) -> None:
    with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for path in sorted(p for p in source.rglob("*") if p.is_file()):
            info = zipfile.ZipInfo(path.relative_to(source).as_posix(), FIXED_TIME)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (path.stat().st_mode & 0xFFFF) << 16
            archive.writestr(info, path.read_bytes())


def make_tar(source: Path, destination: Path) -> None:
    with destination.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                for path in sorted(source.rglob("*")):
                    info = archive.gettarinfo(str(path), arcname=path.relative_to(source).as_posix())
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    info.mtime = 0
                    if path.is_file():
                        with path.open("rb") as handle:
                            archive.addfile(info, handle)
                    else:
                        archive.addfile(info)


def archive_tree(source: Path, destination: Path, platform: str) -> None:
    normalize_tree(source)
    if platform == "windows":
        make_zip(source, destination)
    else:
        make_tar(source, destination)


def build(args: argparse.Namespace) -> list[Path]:
    if args.platform == "windows" and args.arch != "amd64":
        raise SystemExit("Windows arm64 is not a supported release target")
    args.output.mkdir(parents=True, exist_ok=True)
    extension = ".zip" if args.platform == "windows" else ".tar.gz"
    executable = ".exe" if args.platform == "windows" else ""

    with tempfile.TemporaryDirectory() as temp_dir:
        temp = Path(temp_dir)
        service = temp / "service"
        ctl = temp / "ctl"
        copy_binary(args.easy_proxy, service / "bin" / f"easy-proxy{executable}")
        copy_binary(args.ech_workers, service / "bin" / f"ech-workers{executable}")
        copy_binary(args.easyproxyctl, ctl / "bin" / f"easyproxyctl{executable}")
        (service / "VERSION").write_text(args.version + "\n", encoding="utf-8")
        (ctl / "VERSION").write_text(args.version + "\n", encoding="utf-8")
        (service / "config.example.yaml").write_text(render_config(args.platform, args.version), encoding="utf-8")
        shutil.copyfile(ROOT / "docs/native-install-update.md", service / "README.md")
        shutil.copyfile(ROOT / "docs/native-install-update.md", ctl / "README.md")
        if args.platform == "linux":
            install = service / "install"
            install.mkdir()
            shutil.copyfile(ROOT / "deploy/native/linux/install.sh", install / "install.sh")
            shutil.copyfile(ROOT / "deploy/native/linux/easyproxy.service", install / "easyproxy.service")
        else:
            install = service / "install"
            install.mkdir()
            shutil.copyfile(ROOT / "deploy/native/windows/install-service.ps1", install / "install-service.ps1")

        service_archive = args.output / f"easyproxy-{args.platform}-{args.arch}{extension}"
        ctl_archive = args.output / f"easyproxyctl-{args.platform}-{args.arch}{extension}"
        archive_tree(service, service_archive, args.platform)
        archive_tree(ctl, ctl_archive, args.platform)
        return [service_archive, ctl_archive]


if __name__ == "__main__":
    for artifact in build(parse_args()):
        print(artifact)
