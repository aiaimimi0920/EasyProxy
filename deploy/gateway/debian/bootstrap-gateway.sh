#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "bootstrap-gateway.sh must run as root" >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
target_rules=/etc/nftables.d/easyproxy-forwarding.nft
source_rules="${EASYPROXY_FORWARDING_FILE:-${script_dir}/easyproxy-forwarding.nft}"

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y \
  ca-certificates \
  conntrack \
  curl \
  gnupg \
  iproute2 \
  nftables \
  python3-yaml

install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg \
  | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
chmod 0644 /etc/apt/keyrings/docker.gpg

. /etc/os-release
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian %s stable\n' \
  "$(dpkg --print-architecture)" "${VERSION_CODENAME}" \
  > /etc/apt/sources.list.d/docker.list

apt-get update
apt-get install -y \
  containerd.io \
  docker-buildx-plugin \
  docker-ce \
  docker-ce-cli \
  docker-compose-plugin

install -d -m 0755 /etc/nftables.d
if [[ -f "${source_rules}" ]]; then
  install -m 0644 "${source_rules}" "${target_rules}"
elif [[ ! -f "${target_rules}" ]]; then
  echo "missing forwarding rules: ${source_rules}" >&2
  exit 1
fi

cat > /etc/sysctl.d/99-easyproxy-gateway.conf <<'EOF'
net.ipv4.ip_forward=1
net.ipv4.conf.all.send_redirects=0
net.ipv4.conf.default.send_redirects=0
net.ipv6.conf.all.forwarding=1
net.ipv6.conf.default.forwarding=1
net.ipv6.conf.all.accept_ra=2
net.ipv6.conf.default.accept_ra=2
net.ipv6.conf.all.accept_redirects=0
net.ipv6.conf.default.accept_redirects=0
EOF

# Forwarding makes Linux ignore router advertisements unless accept_ra=2 is
# also set on the already-created upstream interface. Persist the interface
# selected by the pre-TUN IPv4 default route so IPv6 has a real bypass route.
while read -r upstream_interface; do
  [[ "$upstream_interface" =~ ^[[:alnum:]_-]+$ ]] || continue
  printf 'net.ipv6.conf.%s.accept_ra=2\n' "$upstream_interface" \
    >> /etc/sysctl.d/99-easyproxy-gateway.conf
done < <(ip -o -4 route show default | awk '{for (i = 1; i <= NF; i++) if ($i == "dev") print $(i + 1)}' | sort -u)

sysctl --system
printf 'include "/etc/nftables.d/*.nft"\n' > /etc/nftables.conf
nft -c -f /etc/nftables.conf
systemctl enable nftables
if ! nft list table inet easyproxy_forwarding >/dev/null 2>&1; then
  systemctl restart nftables
fi
systemctl enable --now docker

if id mjc >/dev/null 2>&1; then
  usermod -aG docker mjc
fi

command -v docker >/dev/null
docker compose version
command -v nft >/dev/null
command -v ip >/dev/null
test "$(sysctl -n net.ipv4.ip_forward)" = 1
test "$(sysctl -n net.ipv6.conf.all.forwarding)" = 1
test -c /dev/net/tun
