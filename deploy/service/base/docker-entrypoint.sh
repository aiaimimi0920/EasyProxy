#!/bin/sh
set -eu

EASY_PROXY_STATE_DIR="${EASY_PROXY_STATE_DIR:-/var/lib/easy-proxy}"
EASY_PROXY_RUNTIME_DIR="${EASY_PROXY_RUNTIME_DIR:-${EASY_PROXY_STATE_DIR}/runtime}"
EASY_PROXY_DATA_DIR="${EASY_PROXY_DATA_DIR:-${EASY_PROXY_STATE_DIR}/data}"
EASY_PROXY_CONNECTORS_DIR="${EASY_PROXY_CONNECTORS_DIR:-${EASY_PROXY_STATE_DIR}/connectors}"
EASY_PROXY_CONFIG_PATH="${EASY_PROXY_CONFIG_PATH:-/etc/easy-proxy/config.yaml}"
EASY_PROXY_DNS_PROXY_BINARY_PATH="${EASY_PROXY_DNS_PROXY_BINARY_PATH:-/usr/local/bin/cloudflared}"
EASY_PROXY_DNS_PROXY_LOG_PATH="${EASY_PROXY_DNS_PROXY_LOG_PATH:-${EASY_PROXY_RUNTIME_DIR}/cloudflared-proxy-dns.log}"
EASY_PROXY_RESOLV_CONF_BACKUP_PATH="${EASY_PROXY_RESOLV_CONF_BACKUP_PATH:-${EASY_PROXY_RUNTIME_DIR}/resolv.conf.original}"
EASY_PROXY_DNS_PROXY_ENABLED="${EASY_PROXY_DNS_PROXY_ENABLED:-0}"
EASY_PROXY_DNS_PROXY_STRICT="${EASY_PROXY_DNS_PROXY_STRICT:-0}"
EASY_PROXY_DNS_PROXY_LISTEN_ADDRESS="${EASY_PROXY_DNS_PROXY_LISTEN_ADDRESS:-127.0.0.1}"
EASY_PROXY_DNS_PROXY_LISTEN_PORT="${EASY_PROXY_DNS_PROXY_LISTEN_PORT:-53}"
EASY_PROXY_DNS_PROXY_UPSTREAM_1="${EASY_PROXY_DNS_PROXY_UPSTREAM_1:-https://dns.google/dns-query}"
EASY_PROXY_DNS_PROXY_UPSTREAM_1_HOST="${EASY_PROXY_DNS_PROXY_UPSTREAM_1_HOST:-dns.google}"
EASY_PROXY_DNS_PROXY_UPSTREAM_1_BOOTSTRAP_IP="${EASY_PROXY_DNS_PROXY_UPSTREAM_1_BOOTSTRAP_IP:-8.8.8.8}"
EASY_PROXY_DNS_PROXY_UPSTREAM_2="${EASY_PROXY_DNS_PROXY_UPSTREAM_2:-https://dns.quad9.net/dns-query}"
EASY_PROXY_DNS_PROXY_UPSTREAM_2_HOST="${EASY_PROXY_DNS_PROXY_UPSTREAM_2_HOST:-dns.quad9.net}"
EASY_PROXY_DNS_PROXY_UPSTREAM_2_BOOTSTRAP_IP="${EASY_PROXY_DNS_PROXY_UPSTREAM_2_BOOTSTRAP_IP:-9.9.9.9}"
EASY_PROXY_DNS_PROXY_STARTUP_SECONDS="${EASY_PROXY_DNS_PROXY_STARTUP_SECONDS:-2}"
EASY_PROXY_RESET_STORE_ON_BOOT="${EASY_PROXY_RESET_STORE_ON_BOOT:-false}"

dns_proxy_pid=""

is_truthy() {
    case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
        1|true|yes|on) return 0 ;;
        *) return 1 ;;
    esac
}

ensure_layout() {
    mkdir -p "${EASY_PROXY_RUNTIME_DIR}" "${EASY_PROXY_DATA_DIR}" "${EASY_PROXY_CONNECTORS_DIR}"
    mkdir -p "$(dirname "${EASY_PROXY_CONFIG_PATH}")"
}

ensure_config_exists() {
    if [ ! -f "${EASY_PROXY_CONFIG_PATH}" ]; then
        echo "error: missing easy-proxy config file: ${EASY_PROXY_CONFIG_PATH}" >&2
        return 1
    fi
}

reset_store_if_requested() {
    if is_truthy "${EASY_PROXY_RESET_STORE_ON_BOOT}"; then
        rm -f "${EASY_PROXY_DATA_DIR}/data.db" "${EASY_PROXY_DATA_DIR}/data.db-shm" "${EASY_PROXY_DATA_DIR}/data.db-wal"
    fi
}

start_dns_proxy_if_enabled() {
    if ! is_truthy "${EASY_PROXY_DNS_PROXY_ENABLED}"; then
        return 0
    fi

    if [ ! -x "${EASY_PROXY_DNS_PROXY_BINARY_PATH}" ]; then
        echo "warning: ${EASY_PROXY_DNS_PROXY_BINARY_PATH} not found; skipping local DoH DNS proxy" >&2
        if is_truthy "${EASY_PROXY_DNS_PROXY_STRICT}"; then
            return 1
        fi
        return 0
    fi

    cp /etc/resolv.conf "${EASY_PROXY_RESOLV_CONF_BACKUP_PATH}" 2>/dev/null || true
    grep -Eq "[[:space:]]${EASY_PROXY_DNS_PROXY_UPSTREAM_1_HOST}([[:space:]]|\$)" /etc/hosts 2>/dev/null || echo "${EASY_PROXY_DNS_PROXY_UPSTREAM_1_BOOTSTRAP_IP} ${EASY_PROXY_DNS_PROXY_UPSTREAM_1_HOST}" >> /etc/hosts
    grep -Eq "[[:space:]]${EASY_PROXY_DNS_PROXY_UPSTREAM_2_HOST}([[:space:]]|\$)" /etc/hosts 2>/dev/null || echo "${EASY_PROXY_DNS_PROXY_UPSTREAM_2_BOOTSTRAP_IP} ${EASY_PROXY_DNS_PROXY_UPSTREAM_2_HOST}" >> /etc/hosts

    "${EASY_PROXY_DNS_PROXY_BINARY_PATH}" proxy-dns \
        --address "${EASY_PROXY_DNS_PROXY_LISTEN_ADDRESS}" \
        --port "${EASY_PROXY_DNS_PROXY_LISTEN_PORT}" \
        --upstream "${EASY_PROXY_DNS_PROXY_UPSTREAM_1}" \
        --upstream "${EASY_PROXY_DNS_PROXY_UPSTREAM_2}" > "${EASY_PROXY_DNS_PROXY_LOG_PATH}" 2>&1 &
    dns_proxy_pid="$!"

    sleep "${EASY_PROXY_DNS_PROXY_STARTUP_SECONDS}"
    if kill -0 "${dns_proxy_pid}" 2>/dev/null; then
        {
            printf 'nameserver %s\n' "${EASY_PROXY_DNS_PROXY_LISTEN_ADDRESS}"
            printf 'options ndots:0 timeout:2 attempts:2\n'
            grep -E '^(search|sortlist)' "${EASY_PROXY_RESOLV_CONF_BACKUP_PATH}" 2>/dev/null || true
        } > /etc/resolv.conf
        return 0
    fi

    dns_proxy_pid=""
    echo "warning: local DoH DNS proxy did not start" >&2
    if is_truthy "${EASY_PROXY_DNS_PROXY_STRICT}"; then
        return 1
    fi
    return 0
}

cleanup() {
    if [ -n "${dns_proxy_pid}" ]; then
        kill "${dns_proxy_pid}" 2>/dev/null || true
    fi
    if [ -f "${EASY_PROXY_RESOLV_CONF_BACKUP_PATH}" ]; then
        cp "${EASY_PROXY_RESOLV_CONF_BACKUP_PATH}" /etc/resolv.conf 2>/dev/null || true
    fi
}

ensure_layout
ensure_config_exists
reset_store_if_requested

if [ "$(id -u)" = "0" ]; then
    trap cleanup EXIT INT TERM
    start_dns_proxy_if_enabled
    chown -R easy:easy "${EASY_PROXY_STATE_DIR}" /etc/easy-proxy
    exec gosu easy /usr/local/bin/easy-proxy "$@"
fi

exec /usr/local/bin/easy-proxy "$@"
