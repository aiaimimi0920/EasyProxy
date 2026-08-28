# EasyProxy NAS Docker deployment

This is the supported NAS deployment path for Linux NAS hosts that provide
Docker Compose. Native Synology/QNAP packages are not published or claimed.

## Supported topology

| Mode | Status | Requirements |
| --- | --- | --- |
| Bridge, Local Server/API | Supported default | amd64 or arm64, ports 22323/29888 |
| Host, transparent gateway | Linux-only advanced path | use `deploy/gateway/debian`, host networking, NET_ADMIN/NET_RAW |
| Windows NAS/native package | Unsupported | no Windows arm64 or NAS-native package |

## Deploy

1. Download this directory and copy `config.template.yaml` from
   `deploy/service/base` to `runtime/config.yaml`.
2. Create the state directory and assign it to the image UID:

   ```sh
   mkdir -p runtime/data
   sudo chown -R 10001:10001 runtime/data
   ```

3. Select an immutable release tag or digest. `latest` is rejected:

   ```sh
   export EASY_PROXY_IMAGE=ghcr.io/YOUR_GITHUB_ACCOUNT/easy-proxy-monorepo-service:v1.0.0
   sh ./preflight.sh
   docker compose up -d
   docker compose ps
   ```

4. Verify `http://NAS_IP:29888/` and configure LAN clients to use
   `NAS_IP:22323`.

The config is mounted read-only at `/etc/easyproxy/config.yaml`; SQLite and
runtime state are mounted at `/var/lib/easyproxy`. Replacing the image does not
replace either bind mount. Before an update, back up both `runtime/config.yaml`
and `runtime/data`; restore both if a migration or health check fails.
