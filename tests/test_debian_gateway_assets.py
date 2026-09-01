from pathlib import Path


ROOT = Path(__file__).parents[1]
ASSET_ROOT = ROOT / "deploy" / "gateway" / "debian"


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
