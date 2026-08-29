import io
import os
import subprocess
import tarfile
import tempfile
from pathlib import Path


ROOT = Path(__file__).parents[1]
INSTALLER = ROOT / "deploy/native/linux/install.sh"
SERVICE = ROOT / "deploy/native/linux/easyproxy.service"


def write_executable(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8", newline="\n")
    path.chmod(0o755)


def add_archive_file(package: tarfile.TarFile, name: str, content: bytes, mode: int) -> None:
    info = tarfile.TarInfo(name)
    info.size = len(content)
    info.mode = mode
    info.mtime = 0
    package.addfile(info, io.BytesIO(content))


def build_package(path: Path) -> None:
    with tarfile.open(path, "w:gz") as package:
        add_archive_file(package, "bin/easy-proxy", b"#!/bin/sh\nexit 0\n", 0o755)
        add_archive_file(package, "config.example.yaml", b"sentinel: packaged\n", 0o644)
        add_archive_file(package, "install/easyproxy.service", SERVICE.read_bytes(), 0o644)


def fake_system_tools(directory: Path) -> None:
    write_executable(
        directory / "id",
        "#!/bin/sh\n"
        'if [ "${1:-}" = "-u" ]; then echo 0; exit 0; fi\n'
        'if [ "${1:-}" = "easyproxy" ]; then exit 0; fi\n'
        'exec /usr/bin/id "$@"\n',
    )
    write_executable(directory / "getent", "#!/bin/sh\nexit 0\n")
    write_executable(directory / "systemctl", "#!/bin/sh\nexit 0\n")
    write_executable(directory / "chown", "#!/bin/sh\nexit 0\n")
    write_executable(directory / "ln", "#!/bin/sh\nexit 0\n")
    write_executable(
        directory / "ss",
        "#!/bin/sh\nprintf 'LISTEN 0 1 0.0.0.0:22323 x\\nLISTEN 0 1 0.0.0.0:29888 x\\n'\n",
    )


def run_installer(
    environment: dict[str, str], fake_bin: Path, *arguments: str
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [
            "sh",
            "-c",
            'PATH="$1:$PATH"; export PATH; shift; exec sh "$@"',
            "easyproxy-installer-test",
            shell_path(fake_bin),
            shell_path(INSTALLER),
            *arguments,
        ],
        cwd=ROOT,
        env=environment,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
        timeout=60,
    )


def shell_path(path: Path) -> str:
    try:
        converted = subprocess.run(
            ["cygpath", "-u", str(path)],
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError:
        return str(path)
    return converted.stdout.strip() if converted.returncode == 0 else str(path)


def test_linux_installer_migrates_preserves_and_rolls_back_config_authority():
    with tempfile.TemporaryDirectory() as temp_dir:
        root = Path(temp_dir)
        install_root = root / "install"
        config_root = root / "etc"
        state_root = root / "state"
        fake_bin = root / "bin"
        unit_path = root / "systemd" / "easyproxy.service"
        archive = root / "easyproxy-linux-amd64.tar.gz"
        fake_bin.mkdir()
        config_root.mkdir()
        (state_root / "data").mkdir(parents=True)
        unit_path.parent.mkdir()
        fake_system_tools(fake_bin)
        build_package(archive)
        (config_root / "config.yaml").write_text("sentinel: legacy\n", encoding="utf-8")
        (state_root / "data/data.db").write_bytes(b"sqlite-legacy")

        environment = os.environ.copy()
        environment.update(
            {
                "PATH": str(fake_bin) + os.pathsep + environment["PATH"],
                "EASYPROXY_INSTALL_ROOT": shell_path(install_root),
                "EASYPROXY_CONFIG_ROOT": shell_path(config_root),
                "EASYPROXY_STATE_ROOT": shell_path(state_root),
                "EASYPROXY_SYSTEMD_UNIT_PATH": shell_path(unit_path),
            }
        )
        installed = run_installer(
            environment, fake_bin, "--version", "v-test", "--archive", shell_path(archive)
        )
        assert installed.returncode == 0, installed.stderr or installed.stdout

        runtime_config = state_root / "config/config.yaml"
        assert runtime_config.read_text(encoding="utf-8") == "sentinel: legacy\n"
        assert (config_root / "config.yaml").read_text(encoding="utf-8") == "sentinel: legacy\n"
        assert "/var/lib/easyproxy/config/config.yaml" in unit_path.read_text(encoding="utf-8")
        backups = list((state_root / "backups").glob("before-v-test-*"))
        assert len(backups) == 1
        assert (backups[0] / "config.yaml").read_text(encoding="utf-8") == "sentinel: legacy\n"
        assert (backups[0] / "bootstrap-config.yaml").exists()

        runtime_config.write_text("sentinel: changed\n", encoding="utf-8")
        (state_root / "data/data.db").write_bytes(b"sqlite-changed")
        rolled_back = run_installer(environment, fake_bin, "--rollback", shell_path(backups[0]))
        assert rolled_back.returncode == 0, rolled_back.stderr or rolled_back.stdout
        assert runtime_config.read_text(encoding="utf-8") == "sentinel: legacy\n"
        assert (state_root / "data/data.db").read_bytes() == b"sqlite-legacy"
