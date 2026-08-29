from pathlib import Path


ROOT = Path(__file__).parents[1]
NAS = ROOT / "deploy/nas"


def test_nas_compose_pins_image_and_persists_config_and_state():
    text = (NAS / "docker-compose.yaml").read_text(encoding="utf-8")
    assert "EASY_PROXY_IMAGE:?Set EASY_PROXY_IMAGE" in text
    assert "/var/lib/easyproxy/config/config.yaml" in text
    assert "/etc/easyproxy/config.yaml:ro" not in text
    assert "/var/lib/easyproxy" in text
    assert '10001' in text
    assert "network_mode: host" not in text


def test_nas_preflight_rejects_latest_and_checks_arch_permissions_ports():
    text = (NAS / "preflight.sh").read_text(encoding="utf-8")
    assert "*:latest" in text
    assert "x86_64|amd64|aarch64|arm64" in text
    assert "stat -c '%u'" in text
    assert "NAS config is not writable" in text
    assert "22323 29888" in text


def test_nas_documentation_has_honest_support_matrix():
    text = (NAS / "README.md").read_text(encoding="utf-8")
    assert "Native Synology/QNAP packages are not published or claimed" in text
    assert "Bridge, Local Server/API" in text
    assert "Host, transparent gateway" in text
    assert "Windows arm64" in text
