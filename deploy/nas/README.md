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

1. Clone the operator's fork and enter this directory, then copy the runtime
   template once:

   ```sh
   git clone --recurse-submodules https://github.com/<OWNER>/<REPOSITORY>.git
   cd <REPOSITORY>/deploy/nas
   mkdir -p runtime
   cp ../service/base/config.template.yaml runtime/config.yaml
   ```
2. Create the state directory and assign both persistent inputs to the image
   UID so WebUI/API changes can update the runtime config:

   ```sh
   mkdir -p runtime/data
   sudo chown -R 10001:10001 runtime
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

The host file `runtime/config.yaml` is mounted as the writable configuration
authority at `/var/lib/easyproxy/config/config.yaml`; SQLite and other runtime
state are also under `/var/lib/easyproxy`. The image copy at
`/etc/easyproxy/config.yaml` is bootstrap-only. Replacing the image does not
replace either host bind mount. Before an update, back up both
`runtime/config.yaml` and `runtime/data`; restore both if a migration or health
check fails.

## Update

Pull the candidate before stopping the current container. Then stop it so the
SQLite copy is consistent, record the exact old image, and create a timestamped
backup:

```sh
container="${EASY_PROXY_CONTAINER_NAME:-easy-proxy}"
new_image='ghcr.io/<OWNER>/easy-proxy-monorepo-service:<NEW_TAG_OR_DIGEST>'
export EASY_PROXY_IMAGE="$new_image"
docker compose pull

old_image="$(docker inspect --format '{{.Config.Image}}' "$container")"
backup="$(pwd)/backups/$(date -u +%Y%m%dT%H%M%SZ)"
docker compose stop
mkdir -p "$backup"
printf '%s\n' "$old_image" > "$backup/image.txt"
cp -p runtime/config.yaml "$backup/config.yaml"
tar -C runtime -czf "$backup/data.tar.gz" data

sh ./preflight.sh
docker compose up -d --force-recreate
docker compose ps
docker inspect --format '{{.State.Health.Status}}' "$container"
```

Do not delete the backup until the management API, LAN HTTP/SOCKS proxy, node
refresh, and SQLite-backed settings have been checked. `healthy` proves only
that the management port accepts TCP; it does not prove proxy semantics.

## Rollback

Use one exact backup directory. Stop the candidate before restoring both config
and data, then return to the recorded image:

```sh
backup='/absolute/path/to/deploy/nas/backups/<TIMESTAMP>'
test -f "$backup/image.txt"
test -f "$backup/config.yaml"
test -f "$backup/data.tar.gz"

docker compose down
cp -p "$backup/config.yaml" runtime/config.yaml
rm -rf runtime/data
tar -C runtime -xzf "$backup/data.tar.gz"
sudo chown -R 10001:10001 runtime
export EASY_PROXY_IMAGE="$(cat "$backup/image.txt")"
sh ./preflight.sh
docker compose up -d --force-recreate
docker compose ps
```

After rollback, repeat the management API, LAN proxy, and SQLite sentinel
checks. Never point two running containers at the same writable `runtime/data`.
