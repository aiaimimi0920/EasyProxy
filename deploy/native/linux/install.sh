#!/bin/sh
set -eu

VERSION=""
BASE_URL=""
ARCHIVE=""
REPLACE_CONFIG=0
ROLLBACK_BACKUP=""
INSTALL_ROOT="${EASYPROXY_INSTALL_ROOT:-/opt/easyproxy}"
CONFIG_ROOT="${EASYPROXY_CONFIG_ROOT:-/etc/easyproxy}"
STATE_ROOT="${EASYPROXY_STATE_ROOT:-/var/lib/easyproxy}"

usage() {
    echo "usage: install.sh --version TAG [--base-url URL | --archive FILE] [--replace-config]"
    echo "       install.sh --rollback BACKUP_DIRECTORY"
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version) VERSION="$2"; shift 2 ;;
        --base-url) BASE_URL="$2"; shift 2 ;;
        --archive) ARCHIVE="$2"; shift 2 ;;
        --replace-config) REPLACE_CONFIG=1; shift ;;
        --rollback) ROLLBACK_BACKUP="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

[ "$(id -u)" = "0" ] || { echo "run as root" >&2; exit 1; }
command -v systemctl >/dev/null
command -v curl >/dev/null
command -v ss >/dev/null

ensure_layout() {
    getent group easyproxy >/dev/null 2>&1 || groupadd --system easyproxy
    id easyproxy >/dev/null 2>&1 || useradd --system --gid easyproxy --home-dir "${STATE_ROOT}" --shell /usr/sbin/nologin easyproxy
    mkdir -p "${INSTALL_ROOT}/releases" "${CONFIG_ROOT}" "${STATE_ROOT}/backups"
    chown -R easyproxy:easyproxy "${STATE_ROOT}"
}

backup_runtime() {
    previous_version="$1"
    backup="$(mktemp -d "${STATE_ROOT}/backups/before-${VERSION:-rollback}-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
    had_config=0
    had_data=0
    if [ -f "${CONFIG_ROOT}/config.yaml" ]; then had_config=1; cp -p "${CONFIG_ROOT}/config.yaml" "${backup}/config.yaml"; fi
    if [ -d "${STATE_ROOT}/data" ]; then had_data=1; cp -a "${STATE_ROOT}/data" "${backup}/data"; fi
    {
        printf 'previous_version=%s\n' "${previous_version}"
        printf 'had_config=%s\n' "${had_config}"
        printf 'had_data=%s\n' "${had_data}"
    } > "${backup}/metadata"
    echo "${backup}"
}

restore_backup() {
    backup="$1"
    backup_real="$(realpath "${backup}")"
    allowed="$(realpath "${STATE_ROOT}/backups")/"
    case "${backup_real}/" in "${allowed}"*) ;; *) echo "backup is outside ${allowed}" >&2; exit 1 ;; esac
    [ -f "${backup_real}/metadata" ] || { echo "invalid backup: ${backup_real}" >&2; exit 1; }
    previous_version="$(sed -n 's/^previous_version=//p' "${backup_real}/metadata")"
    had_config="$(sed -n 's/^had_config=//p' "${backup_real}/metadata")"
    had_data="$(sed -n 's/^had_data=//p' "${backup_real}/metadata")"
    if [ -n "${previous_version}" ]; then
        [ -d "${INSTALL_ROOT}/releases/${previous_version}" ] || { echo "missing release ${previous_version}" >&2; exit 1; }
    fi
    systemctl stop easyproxy.service 2>/dev/null || true
    if [ "${had_config}" = "1" ]; then cp -p "${backup_real}/config.yaml" "${CONFIG_ROOT}/config.yaml"; else rm -f "${CONFIG_ROOT}/config.yaml"; fi
    rm -rf "${STATE_ROOT}/data"
    if [ "${had_data}" = "1" ]; then cp -a "${backup_real}/data" "${STATE_ROOT}/data"; else mkdir -p "${STATE_ROOT}/data"; fi
    chown -R easyproxy:easyproxy "${STATE_ROOT}"
    if [ -n "${previous_version}" ]; then
        ln -sfn "${INSTALL_ROOT}/releases/${previous_version}" "${INSTALL_ROOT}/current"
        systemctl start easyproxy.service
        systemctl is-active --quiet easyproxy.service
    else
        rm -f "${INSTALL_ROOT}/current"
    fi
}

ensure_layout
if [ -n "${ROLLBACK_BACKUP}" ]; then
    current_version=""
    if [ -L "${INSTALL_ROOT}/current" ]; then current_version="$(basename "$(readlink -f "${INSTALL_ROOT}/current")")"; fi
    systemctl stop easyproxy.service 2>/dev/null || true
    safety_backup="$(backup_runtime "${current_version}")"
    restore_backup "${ROLLBACK_BACKUP}"
    echo "EasyProxy rollback completed"
    echo "Pre-rollback safety backup: ${safety_backup}"
    exit 0
fi
[ -n "${VERSION}" ] || { usage >&2; exit 2; }

case "$(uname -m)" in
    x86_64|amd64) PACKAGE_ARCH=amd64 ;;
    aarch64|arm64) PACKAGE_ARCH=arm64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
package="easyproxy-linux-${PACKAGE_ARCH}.tar.gz"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
if [ -z "${ARCHIVE}" ]; then
    [ -n "${BASE_URL}" ] || BASE_URL="https://github.com/aiaimimi0920/EasyProxy/releases/download/${VERSION}"
    curl -fL "${BASE_URL}/${package}" -o "${work}/${package}"
    curl -fL "${BASE_URL}/SHA256SUMS" -o "${work}/SHA256SUMS"
    expected="$(awk -v f="${package}" '$2 == f {print $1}' "${work}/SHA256SUMS")"
    [ -n "${expected}" ] || { echo "checksum missing for ${package}" >&2; exit 1; }
    actual="$(sha256sum "${work}/${package}" | awk '{print $1}')"
    [ "${actual}" = "${expected}" ] || { echo "checksum mismatch for ${package}" >&2; exit 1; }
    ARCHIVE="${work}/${package}"
fi

release_dir="${INSTALL_ROOT}/releases/${VERSION}"
[ ! -e "${release_dir}" ] || { echo "release already installed: ${VERSION}" >&2; exit 1; }
mkdir -p "${release_dir}"
tar -xzf "${ARCHIVE}" -C "${release_dir}"
[ -x "${release_dir}/bin/easy-proxy" ] || { echo "package lacks bin/easy-proxy" >&2; exit 1; }

current_version=""
if [ -L "${INSTALL_ROOT}/current" ]; then current_version="$(basename "$(readlink -f "${INSTALL_ROOT}/current")")"; fi
systemctl stop easyproxy.service 2>/dev/null || true
backup="$(backup_runtime "${current_version}")"
if [ -f "${CONFIG_ROOT}/config.yaml" ]; then
    if [ "${REPLACE_CONFIG}" = "1" ]; then
        cp -p "${CONFIG_ROOT}/config.yaml" "${CONFIG_ROOT}/config.yaml.previous"
        cp "${release_dir}/config.example.yaml" "${CONFIG_ROOT}/config.yaml"
    fi
else
    cp "${release_dir}/config.example.yaml" "${CONFIG_ROOT}/config.yaml"
fi

[ -z "${current_version}" ] || ln -sfn "${INSTALL_ROOT}/releases/${current_version}" "${INSTALL_ROOT}/previous"
ln -sfn "${release_dir}" "${INSTALL_ROOT}/current"
install -m 0644 "${release_dir}/install/easyproxy.service" /etc/systemd/system/easyproxy.service
systemctl daemon-reload
systemctl enable easyproxy.service
if ! systemctl start easyproxy.service || ! systemctl is-active --quiet easyproxy.service; then
    echo "new release failed; restoring ${backup}" >&2
    restore_backup "${backup}"
    rm -rf "${release_dir}"
    exit 1
fi
for port in 22323 29888; do
    attempts=0
    until ss -ltn | grep -Eq ":${port}[[:space:]]"; do
        attempts=$((attempts + 1))
        if [ "${attempts}" -ge 30 ]; then
            echo "port ${port} did not become ready; restoring ${backup}" >&2
            restore_backup "${backup}"
            rm -rf "${release_dir}"
            exit 1
        fi
        sleep 1
    done
done
echo "EasyProxy ${VERSION} installed; rollback backup: ${backup}"
