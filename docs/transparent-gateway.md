# EasyProxy Transparent Gateway

EasyProxy provides a native Linux TUN gateway and retains the legacy transparent
TCP ingress as a rollback mode. It is
network-fabric independent: physical LAN, Tailscale, 星空组网, and future
overlays only need to route trusted client packets to the NAS. EasyProxy does
not call a Tailscale or 星空组网 API and must not be bound to one interface.

## Runtime Contract

Enable `gateway.enabled` only on a Linux/NAS host that can provide:

- host networking, or an equivalent network namespace with forwarding;
- `CAP_NET_ADMIN` and `CAP_NET_RAW`;
- `iproute2` and `nftables` binaries;
- `/dev/net/tun`, policy routing, and nftables support for TUN mode;
- a kernel with `IP_TRANSPARENT` and nftables TPROXY support only for legacy
  `mode: transparent`;
- an ingress route from each trusted physical/overlay CIDR.

Each enabled TUN address family requires at least one trusted client CIDR from
that family. This prevents an apparently healthy IPv4 or IPv6 mode from silently
capturing no clients.

The container image includes `ip` and `nft`, but gateway deployments must set
`EASY_PROXY_RUN_AS_ROOT=1` and add the two capabilities in compose. Ordinary
proxy deployments remain unprivileged. Do not expose `22323`, `29888`, or the
TPROXY listener to WAN or guest networks.

In `mode: tun`, sing-box owns `easyproxy0`; the host supervisor installs IPv4
and optional IPv6 fwmark routes to that interface for configured trusted
interfaces/CIDRs. TCP, UDP, QUIC, and intercepted DNS use the same EasyProxy
route outbound and node pool. Local/private destinations, marked EasyProxy
egress, DHCP/NDP/multicast, and control-plane traffic are bypassed before
capture. Rules are removed on clean shutdown and before reload or TUN teardown.

In legacy `mode: transparent`, the gateway listens on `gateway.listen` (default
`0.0.0.0:15001`) and captures IPv4 TCP through TPROXY.

## Routing And Failure Policy

TUN TCP and UDP use the same rule engine and node pool as explicit HTTP, HTTPS
CONNECT, and SOCKS5 requests. Sniffed or fake-IP-recovered domains are evaluated
before literal destination IPs. TCP and UDP maintain independent failure cooldowns
so a transport-specific failure does not suppress a working path on the same
node. TCP may retain the configured sticky strategy; UDP uses health-first `auto`
selection so one intermittent node cannot pin the whole gateway. UDP first
uses an effectively available native UDP transport such as Hysteria2 or TUIC.
When none is proven available, it follows `no_available_proxy_policy` rather than
silently handing datagrams to an unverified fallback transport.

The default policy is fail-open:

    usable proxy node -> PROXY
    no usable node    -> DIRECT
    gateway stopped   -> normal host forwarding after rule cleanup

`GET /api/gateway/status` reports mode, TUN interface/stack/MTU, IPv4/IPv6,
TCP/UDP/DNS readiness, listener state, legacy transparent connection counters,
and the last lifecycle error. The endpoint is protected by the existing
management authentication.

## Docker/NAS Example

    services:
      easy-proxy:
        network_mode: host
        cap_add:
          - NET_ADMIN
          - NET_RAW
        environment:
          EASY_PROXY_RUN_AS_ROOT: "1"
        volumes:
          - ./config.yaml:/var/lib/easyproxy/config/config.yaml
          - ./data:/var/lib/easyproxy

Tailscale and 星空组网 may run as DSM services, host processes, or separate
host-network containers. They are transport adapters only. Configure their
client routes/exit-node behavior so packets arrive at the NAS; configure
EasyProxy with the resulting interface names and trusted CIDRs. Do not put the
gateway behind a Docker bridge and assume it is a LAN router.

## Safe Rollback

1. Set `gateway.enabled: false` and reload EasyProxy, or stop the container.
2. Confirm `GET /api/gateway/status` reports `applied: false` and
   `tun_ready: false`.
3. On the NAS, inspect `ip rule`, `ip -6 rule`, `ip route show table 100`,
   `ip -6 route show table 101`, and
   `nft list table inet easyproxy_gateway`; the EasyProxy entries must be gone.
4. Restore the client's previous default route/exit-node selection.

Never delete another application's nftables table or policy rule by hand.

## Capability Boundary

TUN mode handles IPv4/IPv6 TCP and UDP, including QUIC/HTTP3, and intercepts
trusted-client TCP/UDP port 53 when `dns_hijack` is enabled. Fake-IP domain
association is persisted under the EasyProxy data directory. ICMP, ESP, GRE,
and other non-TCP/UDP protocols remain DIRECT/bypassed rather than externally
proxied. DoH/DoT are ordinary encrypted TUN flows; certificate-pinned clients
may not provide a recoverable domain, so IP/GeoIP/final rules still apply.

## Validation

Use `scripts/validate-transparent-gateway.ps1` for a read-only management API
check from Windows. Run `scripts/validate-transparent-gateway-linux.sh
--require-tun` on a disposable Linux/NAS host with the gateway configuration; it
checks TUN readiness, IPv4/IPv6 forwarding and policy routes, plus live nftables
TCP/UDP/DNS capture rules. `scripts/validate-tun-gateway-docker.py --build`
creates an isolated dual-stack topology and proves IPv4/IPv6 TCP, UDP, real QUIC,
Fake-IP DNS and SOCKS5 UDP ASSOCIATE. Client-side reachability must still be
tested from each actual LAN/overlay whose default route points at the NAS; the
host validator does not alter client routes.
