from pathlib import Path


ROOT = Path(__file__).parents[1]
STATE_CONFIG = "/var/lib/easyproxy/config/config.yaml"
BOOTSTRAP_CONFIG = "/etc/easyproxy/config.yaml"


def read(relative_path: str) -> str:
    return (ROOT / relative_path).read_text(encoding="utf-8")


def test_container_runtime_config_is_writable_state():
    entrypoint = read("deploy/service/base/docker-entrypoint.sh")
    dockerfile = read("deploy/service/base/Dockerfile")

    assert "${EASY_PROXY_STATE_DIR}/config/config.yaml" in entrypoint
    assert f'EASY_PROXY_BOOTSTRAP_CONFIG_PATH:-{BOOTSTRAP_CONFIG}' in entrypoint
    assert "initialize_runtime_config_if_needed" in entrypoint
    assert f'CMD ["--config", "{STATE_CONFIG}"]' in dockerfile
    assert f"config.template.yaml {BOOTSTRAP_CONFIG}" in dockerfile


def test_official_compose_files_mount_writable_config_authority():
    compose_files = (
        "deploy/service/base/docker-compose.yaml",
        "deploy/nas/docker-compose.yaml",
        "deploy/gateway/debian/docker-compose.yaml",
    )
    for compose_file in compose_files:
        text = read(compose_file)
        lines = text.splitlines()
        matching_lines = [line.strip() for line in lines if STATE_CONFIG in line]
        assert len(matching_lines) == 1, compose_file
        assert not matching_lines[0].endswith(":ro"), compose_file
        parent_index = next(
            index for index, line in enumerate(lines) if "/var/lib/easyproxy" in line and STATE_CONFIG not in line
        )
        child_index = next(index for index, line in enumerate(lines) if STATE_CONFIG in line)
        assert parent_index < child_index, compose_file


def test_linux_native_service_uses_state_config_and_keeps_bootstrap_copy():
    service = read("deploy/native/linux/easyproxy.service")
    installer = read("deploy/native/linux/install.sh")

    assert f"--config {STATE_CONFIG}" in service
    assert 'CONFIG_PATH="${EASYPROXY_CONFIG_PATH:-${STATE_ROOT}/config/config.yaml}"' in installer
    assert 'BOOTSTRAP_CONFIG_PATH="${EASYPROXY_BOOTSTRAP_CONFIG_PATH:-${CONFIG_ROOT}/config.yaml}"' in installer
    assert 'SYSTEMD_UNIT_PATH="${EASYPROXY_SYSTEMD_UNIT_PATH:-/etc/systemd/system/easyproxy.service}"' in installer
    assert "initialize_config_authority" in installer
    assert 'cp "${BOOTSTRAP_CONFIG_PATH}" "${config_temp}"' in installer


def test_docker_smoke_proves_api_write_survives_restart():
    smoke = read("deploy/service/base/scripts/smoke-easy-proxy-docker-api.ps1")

    assert "BootstrapMigration" in smoke
    assert "./config.yaml:${containerConfigPath}" in smoke
    assert "./config.yaml:/etc/easyproxy/config.yaml:ro" in smoke
    assert "bootstrap migration unexpectedly modified" in smoke
    assert "management API update did not persist" in smoke
    assert "docker compose @composeArgs restart" in smoke
    assert "persisted management setting was missing after container restart" in smoke
    assert "SQLite-backed manual node was missing after container restart" in smoke
