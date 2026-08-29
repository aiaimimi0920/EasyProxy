# Debian VM Gateway Deployment

These assets deploy EasyProxy inside the approved Debian VM at
`192.168.15.201`. They do not modify the Synology DSM kernel and do not replace
the existing EasyProxy instance at `192.168.15.200`.

## Host Bootstrap

Copy this directory to the VM and run:

```sh
sudo ./bootstrap-gateway.sh
```

The script installs Docker Engine, enables IPv4 forwarding, disables ICMP
redirects, and installs the `easyproxy_forwarding` nftables table. It does not
create or delete EasyProxy's `easyproxy_gateway` capture table.

## Runtime Layout

Create the runtime directories:

```sh
sudo install -d -m 0750 /opt/easyproxy-gateway/config
sudo install -d -m 0750 /opt/easyproxy-gateway/data
sudo install -d -m 0750 /opt/easyproxy-gateway/compose
```

Place the active runtime configuration at:

```text
/opt/easyproxy-gateway/config/config.yaml
```

Do not commit that file. It contains management credentials and node-source
configuration. Copy `docker-compose.yaml` and a runtime `.env` into the compose
directory, then start the service with:

```sh
cd /opt/easyproxy-gateway/compose
sudo docker compose up -d
```

Set `EASY_PROXY_IMAGE` in the runtime `.env` to an immutable GHCR release tag or
digest. Compose intentionally refuses to start when this value is missing. The
host data directory is mounted at `/var/lib/easyproxy`; the active writable
configuration is mounted at `/var/lib/easyproxy/config/config.yaml`. The image
copy at `/etc/easyproxy/config.yaml` is bootstrap-only.

Keep `gateway.enabled: false` until the explicit proxy at
`192.168.15.201:22323` and the management API at `192.168.15.201:29888` pass
their health checks.

## Synology VMM Disk Note

On the tested Synology VMM vhost-scsi configuration, Debian's ext4 journal can
stall on flush/barrier requests even though the virtual disk is otherwise
usable. The VM used for this deployment therefore has the following runtime
mount option in `/etc/fstab`:

```text
UUID=<root-filesystem-uuid> / ext4 errors=remount-ro,barrier=0 0 1
```

This is a VMM-specific workaround, not a general Debian recommendation. It
reduces storage ordering guarantees and increases the risk of filesystem
corruption after an abrupt power loss. Confirm the VMM storage backend first;
do not copy this option to physical or otherwise healthy virtual disks.

## Gateway Scope

The current transparent data plane captures IPv4 TCP with TPROXY. UDP is
intentionally disabled and IPv6 is not advertised as transparent-gateway
support. Clients that need the gateway should use `192.168.15.201` as their
IPv4 default gateway (or route their overlay IPv4 subnet through that address);
the NAS DSM host at `192.168.15.200` remains a separate container host.
