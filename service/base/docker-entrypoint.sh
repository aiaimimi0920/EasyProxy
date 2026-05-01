#!/bin/sh
set -e

EASY_PROXY_CONFIG_PATH="${EASY_PROXY_CONFIG_PATH:-/etc/easy-proxy/config.yaml}"

# Ensure data directory exists and is writable by the easy user
# When Docker bind-mounts a host directory, it may be owned by root,
# making it inaccessible to the non-root 'easy' user (UID 10001).
# Keep the actual easy-proxy process on EASY_PROXY_CONFIG_PATH even when the
# image CMD still includes the default /etc/easy-proxy/config.yaml pair.
normalized_args=""
skip_next="0"
for arg in "$@"; do
    if [ "${skip_next}" = "1" ]; then
        skip_next="0"
        continue
    fi

    case "${arg}" in
        --config)
            skip_next="1"
            continue
            ;;
        --config=*)
            continue
            ;;
    esac

    normalized_args="${normalized_args}
${arg}"
done

set -- --config "${EASY_PROXY_CONFIG_PATH}"
if [ -n "${normalized_args}" ]; then
    old_ifs="${IFS}"
    IFS='
'
    for arg in ${normalized_args#"
"}; do
        set -- "$@" "${arg}"
    done
    IFS="${old_ifs}"
fi

echo "[easy-proxy] starting with config ${EASY_PROXY_CONFIG_PATH}"

if [ "$(id -u)" = "0" ]; then
    mkdir -p /etc/easy-proxy/data
    chown -R easy:easy /etc/easy-proxy/data

    # Also ensure config files are readable
    chown easy:easy /etc/easy-proxy/config.yaml 2>/dev/null || true
    chown easy:easy /etc/easy-proxy/nodes.txt 2>/dev/null || true

    # Drop privileges and exec as 'easy' user
    exec gosu easy /usr/local/bin/easy-proxy "$@"
fi

# Already running as non-root (e.g. Kubernetes securityContext)
exec /usr/local/bin/easy-proxy "$@"
