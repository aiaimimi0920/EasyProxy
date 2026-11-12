"""Static deployment contracts; these checks do not start containers or alter hosts."""

from pathlib import Path

import pytest
import yaml


ROOT = Path(__file__).parents[1]
NAS = ROOT / "deploy/nas"
ASSET_ROOT = ROOT / "deploy" / "gateway" / "debian"


# NAS bridge deployment

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


# Debian VM transparent gateway and shared service assets

def read_asset(name: str) -> str:
    path = ASSET_ROOT / name
    assert path.is_file(), f"missing Debian gateway asset: {path}"
    return path.read_text(encoding="utf-8")


def test_bootstrap_has_required_host_prerequisites():
    text = read_asset("bootstrap-gateway.sh")

    assert "set -euo pipefail" in text
    assert "net.ipv4.ip_forward=1" in text
    assert "net.ipv4.conf.all.send_redirects=0" in text
    assert "net.ipv4.conf.default.send_redirects=0" in text
    assert "net.ipv6.conf.all.forwarding=1" in text
    assert "net.ipv6.conf.default.forwarding=1" in text
    assert "net.ipv6.conf.all.accept_ra=2" in text
    assert "ip -o -4 route show default" in text
    assert "net.ipv6.conf.%s.accept_ra=2" in text
    assert "net.ipv6.conf.all.accept_redirects=0" in text
    assert "test -c /dev/net/tun" in text
    assert "docker-ce" in text
    assert "nftables" in text
    assert "easyproxy_gateway" not in text
    assert "systemctl enable nftables" in text
    assert "systemctl restart nftables" in text
    assert "systemctl enable --now nftables" not in text
    assert "nft -f /etc/nftables.conf" not in text


def test_forwarding_rules_are_separate_from_easyproxy_capture():
    text = read_asset("easyproxy-forwarding.nft")

    assert "table inet easyproxy_forwarding" in text
    assert "ct state established,related accept" in text
    assert "ip saddr 192.168.15.0/24 accept" in text
    assert "meta l4proto ipv6-icmp accept" in text
    assert 'iifname "ens3" meta nfproto ipv6 accept' in text
    assert "masquerade" in text
    assert "easyproxy_gateway" not in text


def test_compose_uses_host_network_and_only_gateway_capabilities():
    text = read_asset("docker-compose.yaml")

    assert "network_mode: host" in text
    assert "NET_ADMIN" in text
    assert "NET_RAW" in text
    assert "/dev/net/tun:/dev/net/tun" in text
    assert "EASY_PROXY_RUN_AS_ROOT" in text
    assert "ports:" not in text
    assert "context:" not in text
    assert "/opt/easyproxy-gateway/config/config.yaml" in text
    assert "EASY_PROXY_IMAGE:?Set EASY_PROXY_IMAGE" in text
    assert "/opt/easyproxy-gateway/data:/var/lib/easyproxy" in text


def test_service_entrypoint_does_not_chown_bootstrap_config():
    text = (ROOT / "deploy" / "service" / "base" / "docker-entrypoint.sh").read_text(encoding="utf-8")

    assert 'chown -R easy:easy "${EASY_PROXY_STATE_DIR}"' in text
    assert 'chown -R easy:easy "${EASY_PROXY_STATE_DIR}" /etc/easyproxy' not in text


def test_service_entrypoint_honors_gateway_root_opt_in():
    text = (ROOT / "deploy" / "service" / "base" / "docker-entrypoint.sh").read_text(encoding="utf-8")

    assert 'EASY_PROXY_RUN_AS_ROOT' in text
    assert 'if is_truthy "${EASY_PROXY_RUN_AS_ROOT:-0}"; then' in text


def test_service_template_has_no_placeholder_sources():
    text = (ROOT / "deploy" / "service" / "base" / "config.template.yaml").read_text(encoding="utf-8")

    assert "source_sync:\n  enabled: false" in text
    assert "subscriptions: []" in text
    assert "https://example.com/subscription" not in text


def test_gateway_image_contains_host_networking_tools():
    text = (ROOT / "deploy" / "service" / "base" / "Dockerfile").read_text(encoding="utf-8")

    assert "iproute2" in text
    assert "nftables" in text


def test_native_tun_validator_checks_dual_stack_runtime():
    text = (ROOT / "scripts" / "validate-transparent-gateway-linux.sh").read_text(encoding="utf-8")

    assert "--require-tun" in text
    assert "/dev/net/tun" in text
    assert "net/ipv6/conf/all/forwarding" in text
    assert 'ip -6 route show table 101' in text
    assert 'fwmark 0x1/0x1 lookup 101' in text
    assert 'meta l4proto { tcp, udp }' in text
    assert 'th dport 53' in text


def test_ci_runs_native_tun_dual_stack_e2e():
    workflow = (ROOT / ".github" / "workflows" / "reusable-validate.yml").read_text(encoding="utf-8")
    validator = ROOT / "scripts" / "validate-tun-gateway-docker.py"

    assert validator.is_file()
    assert "native-tun-e2e:" in workflow
    assert "Build the native TUN E2E image" in workflow
    assert "validate-tun-gateway-docker.py --image easyproxy-native-tun:e2e" in workflow
    assert "PASS real-quic" in validator.read_text(encoding="utf-8")


def test_preseed_keeps_credentials_runtime_only_and_pins_gateway_network():
    text = (ASSET_ROOT / "preseed.cfg.example").read_text(encoding="utf-8")
    assert "__MJC_PASSWORD_HASH__" in text
    assert "passwd/user-password password" not in text
    assert "192.168.15.201" in text
    assert "192.168.15.1" in text
    assert "mirror/http/proxy string http://192.168.15.200:22323" in text
    assert "openssh-server" in text
    assert "partman-auto/disk string /dev/sda" in text
    assert "grub-installer/bootdev string /dev/sda" in text


# Log limits shared by all official Compose layouts

@pytest.mark.parametrize("layout", ["gateway/debian", "nas", "service/base"])
def test_official_compose_layouts_bound_container_logs(layout):
    document = yaml.safe_load((ROOT / "deploy" / layout / "docker-compose.yaml").read_text(encoding="utf-8"))
    service = next(iter(document["services"].values()))
    assert service["logging"] == {
        "driver": "json-file",
        "options": {
            "max-size": "${EASY_DOCKER_LOG_MAX_SIZE:-20m}",
            "max-file": "${EASY_DOCKER_LOG_MAX_FILE:-5}",
        },
    }
