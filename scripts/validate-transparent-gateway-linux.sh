#!/usr/bin/env bash
set -euo pipefail

MANAGEMENT_URL="http://127.0.0.1:29888"
PASSWORD=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --management-url) MANAGEMENT_URL="$2"; shift 2 ;;
    --password) PASSWORD="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

command -v ip >/dev/null || { echo "missing iproute2" >&2; exit 1; }
command -v nft >/dev/null || { echo "missing nftables" >&2; exit 1; }
[[ -r /proc/sys/net/ipv4/ip_forward ]] || { echo "IPv4 forwarding sysctl unavailable" >&2; exit 1; }
if [[ "$(cat /proc/sys/net/ipv4/ip_forward)" != "1" ]]; then
  echo "IPv4 forwarding is disabled; enable it in the NAS host configuration before gateway use" >&2
  exit 1
fi

auth_args=()
if [[ -n "$PASSWORD" ]]; then auth_args+=( -H "Authorization: $PASSWORD" ); fi
curl --fail --silent --show-error "${auth_args[@]}" "${MANAGEMENT_URL%/}/api/gateway/status" | tee /tmp/easyproxy-gateway-status.json

if nft list table inet easyproxy_gateway >/dev/null 2>&1; then
  echo "nftables table inet easyproxy_gateway is present"
else
  echo "nftables table inet easyproxy_gateway is absent (gateway may be disabled)"
fi
ip rule show | grep -F "fwmark 0x1/0x1 lookup 100" || true
ip route show table 100 || true
echo "Prerequisite/status check passed. Test Google reachability from a physical-LAN client and an overlay client separately; this script does not rewrite their routes."
