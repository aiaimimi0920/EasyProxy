# Debian VM Gateway Deployment

These assets deploy EasyProxy inside the approved Debian VM at
`192.168.15.201`. They do not modify the Synology DSM kernel and do not replace
the existing EasyProxy instance at `192.168.15.200`.

## Host Bootstrap

Copy this directory to the VM and run:

```sh
sudo ./bootstrap-gateway.sh
```

The script installs Docker Engine, enables IPv4 and IPv6 forwarding, preserves
IPv6 router advertisements for the upstream LAN, disables redirects, verifies
`/dev/net/tun`, and installs the `easyproxy_forwarding` nftables table. It does
not create or delete EasyProxy's `easyproxy_gateway` capture table.

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

`gateway.mode: tun` uses EasyProxy's embedded sing-box TUN inbound for IPv4 and
IPv6 TCP/UDP. DNS port 53 can be hijacked into the same DNS engine, and fake-IP
association preserves domains for later routing. QUIC/HTTP3 and other UDP flows
use only UDP-capable pool members and follow the configured DIRECT fallback.

The forwarding fixture assumes the VM's trusted LAN interface is `ens3`; change
that single nftables interface value before bootstrap when the VM uses another
name. Do not broaden it to a WAN-facing interface. The provider-neutral template
describes both address families but leaves the gateway disabled and trusted CIDRs
empty. Before enabling it, either verify IPv6 egress and add the LAN IPv6 CIDR,
or set `gateway.tun.ipv6: false`.

Clients use `192.168.15.201` as their IPv4 default gateway and the VM's routed
IPv6 address as their IPv6 next hop, or route an overlay subnet through those
addresses. The NAS DSM host at `192.168.15.200` remains a separate container
host.

Rollback to the legacy path by setting `gateway.mode: transparent`, disabling
all `gateway.tun` capture features, and restoring `capture.tcp: tproxy`. After a
restart, verify `easyproxy0` is absent, tables 100/101 contain no EasyProxy
routes, and `nft list table inet easyproxy_gateway` contains only the expected
TPROXY rules.
