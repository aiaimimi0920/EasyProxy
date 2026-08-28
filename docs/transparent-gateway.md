# EasyProxy Transparent Gateway

EasyProxy Phase 1 provides a native Linux transparent TCP ingress. It is
network-fabric independent: physical LAN, Tailscale, 星空组网, and future
overlays only need to route trusted client packets to the NAS. EasyProxy does
not call a Tailscale or 星空组网 API and must not be bound to one interface.

## Runtime Contract

Enable `gateway.enabled` only on a Linux/NAS host that can provide:

- host networking, or an equivalent network namespace with forwarding;
- `CAP_NET_ADMIN` and `CAP_NET_RAW`;
- `iproute2` and `nftables` binaries;
- a kernel with `IP_TRANSPARENT`, policy routing, and nftables TPROXY support;
- an ingress route from each trusted physical/overlay CIDR.

The container image includes `ip` and `nft`, but gateway deployments must set
`EASY_PROXY_RUN_AS_ROOT=1` and add the two capabilities in compose. Ordinary
proxy deployments remain unprivileged. Do not expose `22323`, `29888`, or the
TPROXY listener to WAN or guest networks.

The gateway listens on `gateway.listen` (default `0.0.0.0:15001`). The host
supervisor installs an fwmark route and nftables rules for configured trusted
interfaces/CIDRs. Local/private destinations, marked EasyProxy egress, and
control-plane traffic are bypassed before capture. Rules are removed on clean
shutdown, reload, and listener startup failure.

## Routing And Failure Policy

Transparent TCP uses the same rule engine and node pool as explicit HTTP,
HTTPS CONNECT, and SOCKS5 requests. The original destination is taken from the
transparent socket; the client source address remains available for device and
session mapping.

The default policy is fail-open:

    usable proxy node -> PROXY
    no usable node    -> DIRECT
    gateway stopped   -> normal host forwarding after rule cleanup

`GET /api/gateway/status` reports listener state, active connections, DIRECT /
PROXY counts, DIRECT fallbacks, and the last lifecycle error. The endpoint is
protected by the existing management authentication.

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
          - ./config.yaml:/etc/easyproxy/config.yaml
          - ./data:/etc/easyproxy/data

Tailscale and 星空组网 may run as DSM services, host processes, or separate
host-network containers. They are transport adapters only. Configure their
client routes/exit-node behavior so packets arrive at the NAS; configure
EasyProxy with the resulting interface names and trusted CIDRs. Do not put the
gateway behind a Docker bridge and assume it is a LAN router.

## Safe Rollback

1. Set `gateway.enabled: false` and reload EasyProxy, or stop the container.
2. Confirm `GET /api/gateway/status` reports `applied: false`.
3. On the NAS, inspect `ip rule`, `ip route show table 100`, and
   `nft list table inet easyproxy_gateway`; the EasyProxy entries must be gone.
4. Restore the client's previous default route/exit-node selection.

Never delete another application's nftables table or policy rule by hand.

## Capability Boundary

Phase 1 intentionally supports transparent TCP only. UDP/UDP ASSOCIATE, QUIC,
WebRTC-like flows, IPv6 forwarding/capture, and DNS interception are not
claimed as complete. DoH/DoT can bypass any port-53 resolver. These are Phase 2
work and require a real UDP-capable outbound path rather than a TCP wrapper.

## Validation

Use `scripts/validate-transparent-gateway.ps1` for a read-only management API
check from Windows. Run `scripts/validate-transparent-gateway-linux.sh` on a
disposable Linux/NAS host with the gateway configuration; it checks kernel
prerequisites, forwarding, the management status endpoint, and cleanup state.
Client-side Google reachability must be tested from one physical-LAN client and
one overlay client whose default route points at the NAS; the validation script
does not alter those routes.
