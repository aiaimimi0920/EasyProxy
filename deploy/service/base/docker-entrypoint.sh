#!/bin/sh
set -eu

EASY_PROXY_STATE_DIR="${EASY_PROXY_STATE_DIR:-/var/lib/easy-proxy}"
EASY_PROXY_RUNTIME_DIR="${EASY_PROXY_RUNTIME_DIR:-${EASY_PROXY_STATE_DIR}/runtime}"
EASY_PROXY_DATA_DIR="${EASY_PROXY_DATA_DIR:-${EASY_PROXY_STATE_DIR}/data}"
EASY_PROXY_CONNECTORS_DIR="${EASY_PROXY_CONNECTORS_DIR:-${EASY_PROXY_STATE_DIR}/connectors}"
EASY_PROXY_CONFIG_PATH="${EASY_PROXY_CONFIG_PATH:-/etc/easy-proxy/config.yaml}"
EASY_PROXY_BOOTSTRAP_PATH="${EASY_PROXY_BOOTSTRAP_PATH:-/etc/easy-proxy/bootstrap/r2-bootstrap.json}"
EASY_PROXY_IMPORT_CODE="${EASY_PROXY_IMPORT_CODE:-}"
EASY_PROXY_IMPORT_STATE_PATH="${EASY_PROXY_IMPORT_STATE_PATH:-${EASY_PROXY_STATE_DIR}/import-sync-state.json}"
EASY_PROXY_IMPORT_SYNC_FLAG_PATH="${EASY_PROXY_IMPORT_SYNC_FLAG_PATH:-${EASY_PROXY_STATE_DIR}/import-sync.restart}"
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
app_pid=""
sync_pid=""

is_truthy() {
    case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
        1|true|yes|on) return 0 ;;
        *) return 1 ;;
    esac
}

ensure_layout() {
    mkdir -p "${EASY_PROXY_RUNTIME_DIR}" "${EASY_PROXY_DATA_DIR}" "${EASY_PROXY_CONNECTORS_DIR}"
    mkdir -p "$(dirname "${EASY_PROXY_CONFIG_PATH}")"
    mkdir -p "$(dirname "${EASY_PROXY_BOOTSTRAP_PATH}")"
}

ensure_config_exists() {
    if [ ! -f "${EASY_PROXY_CONFIG_PATH}" ]; then
        echo "error: missing easy-proxy config file: ${EASY_PROXY_CONFIG_PATH}" >&2
        return 1
    fi
}

generate_bootstrap_from_import_code() {
    if [ -f "${EASY_PROXY_BOOTSTRAP_PATH}" ] || [ -z "${EASY_PROXY_IMPORT_CODE}" ]; then
        return 0
    fi

    echo "[easy-proxy] import code provided, generating bootstrap file at ${EASY_PROXY_BOOTSTRAP_PATH}"
    python /usr/local/bin/easyproxy-import-code.py inspect \
        --import-code "${EASY_PROXY_IMPORT_CODE}" \
        --output "${EASY_PROXY_BOOTSTRAP_PATH}"
}

bootstrap_runtime_config_if_needed() {
    if [ -f "${EASY_PROXY_CONFIG_PATH}" ]; then
        return 0
    fi

    if [ -f "${EASY_PROXY_BOOTSTRAP_PATH}" ]; then
        echo "[easy-proxy] runtime config missing, attempting bootstrap via ${EASY_PROXY_BOOTSTRAP_PATH}"
        python /usr/local/bin/bootstrap-service-config.py \
            --bootstrap-path "${EASY_PROXY_BOOTSTRAP_PATH}" \
            --config-path "${EASY_PROXY_CONFIG_PATH}" \
            --state-path "${EASY_PROXY_IMPORT_STATE_PATH}"
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
    if [ -n "${sync_pid}" ]; then
        kill "${sync_pid}" 2>/dev/null || true
        wait "${sync_pid}" 2>/dev/null || true
    fi
    if [ -n "${app_pid}" ]; then
        kill "${app_pid}" 2>/dev/null || true
        wait "${app_pid}" 2>/dev/null || true
    fi
    if [ -n "${dns_proxy_pid}" ]; then
        kill "${dns_proxy_pid}" 2>/dev/null || true
    fi
    if [ -f "${EASY_PROXY_RESOLV_CONF_BACKUP_PATH}" ]; then
        cp "${EASY_PROXY_RESOLV_CONF_BACKUP_PATH}" /etc/resolv.conf 2>/dev/null || true
    fi
}

resolve_bootstrap_sync_setting() {
    python - "${EASY_PROXY_BOOTSTRAP_PATH}" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
if not path.exists():
    print("false")
    print("7200")
    raise SystemExit(0)

payload = json.loads(path.read_text(encoding="utf-8-sig"))
print("true" if payload.get("syncEnabled", True) else "false")
print(int(payload.get("syncIntervalSeconds") or 7200))
PY
}

start_runtime() {
    if [ "$(id -u)" = "0" ]; then
        chown -R easy:easy "${EASY_PROXY_STATE_DIR}" /etc/easy-proxy
        gosu easy /usr/local/bin/easy-proxy "$@" &
    else
        /usr/local/bin/easy-proxy "$@" &
    fi
    app_pid="$!"
}

start_sync_loop() {
    sync_interval_seconds="$1"
    (
        while true; do
            sleep "${sync_interval_seconds}"
            python /usr/local/bin/bootstrap-service-config.py \
                --bootstrap-path "${EASY_PROXY_BOOTSTRAP_PATH}" \
                --config-path "${EASY_PROXY_CONFIG_PATH}" \
                --state-path "${EASY_PROXY_IMPORT_STATE_PATH}" \
                --mode sync \
                --updated-flag-path "${EASY_PROXY_IMPORT_SYNC_FLAG_PATH}"
            if [ -f "${EASY_PROXY_IMPORT_SYNC_FLAG_PATH}" ]; then
                echo "[easy-proxy] remote runtime config updated, restarting service"
                kill "${app_pid}" 2>/dev/null || true
                break
            fi
        done
    ) &
    sync_pid="$!"
}

ensure_layout
reset_store_if_requested
generate_bootstrap_from_import_code
bootstrap_runtime_config_if_needed
ensure_config_exists

if [ "$(id -u)" = "0" ]; then
    trap cleanup EXIT INT TERM
    start_dns_proxy_if_enabled
fi

SYNC_ENABLED="false"
SYNC_INTERVAL_SECONDS="7200"
if [ -f "${EASY_PROXY_BOOTSTRAP_PATH}" ]; then
    SYNC_VALUES="$(resolve_bootstrap_sync_setting)"
    SYNC_ENABLED="$(printf '%s' "$SYNC_VALUES" | sed -n '1p')"
    SYNC_INTERVAL_SECONDS="$(printf '%s' "$SYNC_VALUES" | sed -n '2p')"
fi

while true; do
    rm -f "${EASY_PROXY_IMPORT_SYNC_FLAG_PATH}"
    start_runtime "$@"
    if [ "${SYNC_ENABLED}" = "true" ] && [ -f "${EASY_PROXY_BOOTSTRAP_PATH}" ]; then
        start_sync_loop "${SYNC_INTERVAL_SECONDS}"
    else
        sync_pid=""
    fi

    APP_STATUS=0
    wait "${app_pid}" || APP_STATUS=$?
    app_pid=""

    if [ -n "${sync_pid}" ]; then
        kill "${sync_pid}" 2>/dev/null || true
        wait "${sync_pid}" 2>/dev/null || true
        sync_pid=""
    fi

    if [ -f "${EASY_PROXY_IMPORT_SYNC_FLAG_PATH}" ]; then
        rm -f "${EASY_PROXY_IMPORT_SYNC_FLAG_PATH}"
        continue
    fi

    exit "${APP_STATUS}"
done
