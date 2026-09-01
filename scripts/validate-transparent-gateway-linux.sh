#!/usr/bin/env bash
set -euo pipefail

MANAGEMENT_URL="http://127.0.0.1:29888"
PASSWORD=""
REQUIRE_TUN=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --management-url) MANAGEMENT_URL="$2"; shift 2 ;;
    --password) PASSWORD="$2"; shift 2 ;;
    --require-tun) REQUIRE_TUN=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

command -v ip >/dev/null || { echo "missing iproute2" >&2; exit 1; }
command -v nft >/dev/null || { echo "missing nftables" >&2; exit 1; }
command -v python3 >/dev/null || { echo "missing python3" >&2; exit 1; }
[[ -r /proc/sys/net/ipv4/ip_forward ]] || { echo "IPv4 forwarding sysctl unavailable" >&2; exit 1; }
if [[ "$(cat /proc/sys/net/ipv4/ip_forward)" != "1" ]]; then
  echo "IPv4 forwarding is disabled; enable it in the NAS host configuration before gateway use" >&2
  exit 1
fi

auth_args=()
if [[ -n "$PASSWORD" ]]; then auth_args+=( -H "Authorization: $PASSWORD" ); fi
status_file="$(mktemp)"
trap 'rm -f "$status_file"' EXIT
curl --fail --silent --show-error "${auth_args[@]}" "${MANAGEMENT_URL%/}/api/gateway/status" | tee "$status_file"
echo

status_value() {
  python3 - "$status_file" "$1" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    value = json.load(handle).get(sys.argv[2])
if isinstance(value, bool):
    print(str(value).lower())
elif value is not None:
    print(value)
PY
}

enabled="$(status_value enabled)"
applied="$(status_value applied)"
mode="$(status_value mode)"
if [[ "$REQUIRE_TUN" == 1 && "$mode" != "tun" ]]; then
  echo "gateway mode is ${mode:-unset}, expected tun" >&2
  exit 1
fi
if [[ "$enabled" == "true" && "$applied" != "true" ]]; then
  echo "gateway is enabled but capture is not applied" >&2
  exit 1
fi

if [[ "$mode" == "tun" ]]; then
  [[ "$applied" == "true" ]] || { echo "TUN gateway is not applied" >&2; exit 1; }
  [[ "$(status_value tun_ready)" == "true" ]] || { echo "TUN runtime is not ready" >&2; exit 1; }
  tun_interface="$(status_value interface)"
  [[ -n "$tun_interface" ]] || { echo "TUN interface is absent from gateway status" >&2; exit 1; }
  [[ -c /dev/net/tun ]] || { echo "/dev/net/tun is unavailable" >&2; exit 1; }
  ip link show dev "$tun_interface" >/dev/null

  nft_rules="$(nft list table inet easyproxy_gateway)"
  grep -F "meta l4proto { tcp, udp }" <<<"$nft_rules" >/dev/null
  if [[ "$(status_value dns)" == "true" ]]; then
    grep -F "th dport 53" <<<"$nft_rules" >/dev/null
  fi

  if [[ "$(status_value ipv4)" == "true" ]]; then
    grep -F "fwmark 0x1/0x1 lookup 100" < <(ip rule show) >/dev/null
    ip route show table 100 | grep -F "default dev ${tun_interface}" >/dev/null
  fi
  if [[ "$(status_value ipv6)" == "true" ]]; then
    [[ -r /proc/sys/net/ipv6/conf/all/forwarding ]] || { echo "IPv6 forwarding sysctl unavailable" >&2; exit 1; }
    [[ "$(cat /proc/sys/net/ipv6/conf/all/forwarding)" == "1" ]] || { echo "IPv6 forwarding is disabled" >&2; exit 1; }
    grep -F "fwmark 0x1/0x1 lookup 101" < <(ip -6 rule show) >/dev/null
    ip -6 route show table 101 | grep -F "default dev ${tun_interface}" >/dev/null
  fi
  echo "Native TUN host validation passed: IPv4/IPv6 policy routing and TCP/UDP/DNS capture are ready."
else
  if nft list table inet easyproxy_gateway >/dev/null 2>&1; then
    echo "nftables table inet easyproxy_gateway is present"
  else
    echo "nftables table inet easyproxy_gateway is absent (gateway may be disabled)"
  fi
  echo "Gateway prerequisite/status check passed."
fi
