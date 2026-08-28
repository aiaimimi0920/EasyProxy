#!/bin/sh
set -eu

IMAGE="${EASY_PROXY_IMAGE:-}"
CONFIG_PATH="${EASY_PROXY_CONFIG_PATH:-./runtime/config.yaml}"
STATE_PATH="${EASY_PROXY_STATE_PATH:-./runtime/data}"
EXPECTED_UID="${EASY_PROXY_UID:-10001}"

case "${IMAGE}" in
    ""|*:latest) echo "EASY_PROXY_IMAGE must be an explicit version tag or digest" >&2; exit 1 ;;
    *@sha256:*) printf '%s\n' "${IMAGE}" | grep -Eq '@sha256:[0-9a-fA-F]{64}$' || { echo "invalid sha256 image digest" >&2; exit 1; } ;;
    *:* ) ;;
    *) echo "EASY_PROXY_IMAGE must include a version tag or digest" >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64|amd64|aarch64|arm64) ;;
    *) echo "unsupported NAS architecture: $(uname -m)" >&2; exit 1 ;;
esac

docker compose version >/dev/null
if [ ! -f "${CONFIG_PATH}" ]; then
    echo "missing NAS config: ${CONFIG_PATH}" >&2
    exit 1
fi
mkdir -p "${STATE_PATH}"
if [ ! -w "${STATE_PATH}" ]; then
    echo "NAS state directory is not writable: ${STATE_PATH}" >&2
    exit 1
fi

if command -v stat >/dev/null 2>&1; then
    owner_uid="$(stat -c '%u' "${STATE_PATH}" 2>/dev/null || stat -f '%u' "${STATE_PATH}")"
    if [ "${owner_uid}" != "${EXPECTED_UID}" ]; then
        echo "${STATE_PATH} must be owned by UID ${EXPECTED_UID}; current UID is ${owner_uid}" >&2
        exit 1
    fi
fi

ports="$(docker ps --format '{{.Ports}}')"
for port in 22323 29888; do
    if printf '%s\n' "${ports}" | grep -Eq "(^|[.:])${port}->"; then
        echo "host port ${port} is already published by another container" >&2
        exit 1
    fi
done

echo "NAS preflight passed for ${IMAGE}"
