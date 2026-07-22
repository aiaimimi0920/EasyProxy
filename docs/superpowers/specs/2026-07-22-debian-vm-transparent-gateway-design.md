# Debian VM Transparent Gateway Design

**Date:** 2026-07-22

**Status:** Approved

## Goal

Run EasyProxy as a shared transparent gateway inside a Debian virtual machine
hosted by Synology Virtual Machine Manager on the DS920+. The deployment must
avoid the DSM 4.4 kernel's missing nftables TPROXY support, preserve the existing
EasyProxy container on `192.168.15.200`, and provide an isolated gateway at
`192.168.15.201` for physical-LAN and overlay clients.

## Constraints

- The existing DSM EasyProxy instance remains available throughout deployment.
- DSM kernel modules and the DSM kernel itself are not modified.
- EasyProxy remains independent of Tailscale, StarVPN, or any other overlay
  vendor. Overlay software only transports packets to the gateway.
- The first release transparently proxies IPv4 TCP. UDP is forwarded DIRECT.
- An empty or unusable proxy pool fails open to DIRECT.
- IPv6 is outside the first deployment boundary and must not be presented as
  transparently proxied.
- Management and proxy listeners are exposed only to trusted LAN and overlay
  source ranges.

## Host Baseline

The verified NAS baseline is:

```text
Model:             Synology DS920+
Platform:          synology_geminilake_920+
DSM:               7.2-64570 Update 4
Kernel:            Linux 4.4.302+
Memory:            approximately 20 GiB
Available memory:  approximately 15.8 GiB
Volume 1 free:     approximately 469 GiB
LAN bridge:        ovs_bond0
NAS address:       192.168.15.200/24
Router:            192.168.15.1
```

The CPU exposes VMX and the DSM image includes `kvm.ko` and `kvm-intel.ko`.
Virtual Machine Manager is available from Synology Package Center as
`Virtualization` version `2.7.0-12229`, but was not installed when this design
was approved.

## Selected Architecture

Install Synology Virtual Machine Manager and create one Debian 12 amd64 VM:

```text
Name:              easyproxy-gateway
vCPU:              2
Memory:            4 GiB
Disk:              32 GiB on /volume1
Network:           one virtual NIC bridged to ovs_bond0
Address:           192.168.15.201/24
Default gateway:   192.168.15.1
Initial DNS:       192.168.15.1
```

Docker Engine runs inside the VM. EasyProxy runs with host networking and only
the capabilities needed to own policy routing and nftables state:

```yaml
network_mode: host
cap_add:
  - NET_ADMIN
  - NET_RAW
environment:
  EASY_PROXY_RUN_AS_ROOT: "1"
```

The current DSM deployment remains available at `192.168.15.200`. The new VM
deployment uses `192.168.15.201`, so both instances can run concurrently without
port remapping or container-name conflicts.

## Packet Flow

For a physical LAN client that selects `192.168.15.201` as its default gateway:

```text
client
  -> Debian VM PREROUTING
  -> EasyProxy nftables TPROXY rule for trusted IPv4 TCP
  -> EasyProxy transparent listener
  -> routing policy
     -> DIRECT dial from the VM, or
     -> PROXY dial through a healthy EasyProxy node
  -> 192.168.15.1
  -> Internet
```

Traffic not captured by the current TCP gateway, including UDP, is forwarded by
the Debian VM and masqueraded through its LAN address. UDP/443 may be rejected
for trusted test clients so browsers fall back from QUIC to TCP/HTTPS and enter
the EasyProxy path.

Local, management, control-plane, proxy-node return, and reserved destinations
must be bypassed before transparent capture. EasyProxy's nftables table remains
separate from the VM's base forwarding/NAT table.

## Overlay Independence

Physical LAN, Tailscale, StarVPN, and future overlays are ingress transports.
EasyProxy configuration contains only interface names, interface patterns,
trusted CIDRs, and optional device address aliases.

The first acceptance run uses the physical LAN. Overlay integration is added
only after the physical path passes. An overlay may run inside Debian or route
traffic from another host to the VM, but no overlay API or product-specific
logic is added to the EasyProxy core.

## Base Gateway State

The Debian VM owns persistent host networking outside the container lifecycle:

- enable `net.ipv4.ip_forward=1`;
- disable IPv4 ICMP redirects;
- allow forwarding from configured trusted source ranges;
- masquerade forwarded traffic leaving through the LAN interface;
- optionally reject trusted-ingress UDP/443 during transparent proxy tests;
- preserve SSH and EasyProxy management access from trusted ranges.

These rules use a dedicated nftables table that is not named
`easyproxy_gateway`. EasyProxy owns only its policy-routing and transparent
capture state, so stopping the container cannot remove the VM's base routing
and NAT rules.

## Addresses and Ports

The new deployment exposes:

```text
22/tcp:     Debian SSH from trusted networks
22323/tcp:  EasyProxy explicit HTTP/HTTPS/SOCKS listener
29888/tcp:  EasyProxy authenticated management console
15001/tcp:  EasyProxy transparent listener
```

The EasyProxy management password remains the user-selected value already used
by the DSM deployment. Debian administration should use an SSH public key;
plaintext system credentials must not be committed to the repository or stored
in persistent provisioning files.

## Capability Boundary

The initial production boundary is:

- IPv4 TCP transparent capture: supported;
- explicit HTTP/HTTPS/SOCKS proxy: supported;
- proxy-pool fail-open to DIRECT: supported;
- IPv4 UDP forwarding through the VM: supported as DIRECT traffic;
- transparent UDP proxying and SOCKS5 UDP ASSOCIATE: not supported yet;
- IPv6 transparent capture: not supported yet.

During acceptance testing, the test client uses IPv4. IPv6 must be disabled on
that client or otherwise prevented from becoming an unobserved bypass path.

## Deployment Safety

Deployment is parallel and reversible:

1. Install VMM without stopping the current EasyProxy container.
2. Create the VM and validate ordinary networking before installing EasyProxy.
3. Validate the new explicit proxy before enabling transparent capture.
4. Move only one test client to the new default gateway.
5. Restore that client's default gateway to `192.168.15.1` for immediate
   rollback.
6. Keep the DSM EasyProxy instance running until the VM passes all acceptance
   tests and an explicit later decision is made about retirement.

Create VM snapshots after Debian installation, after Docker installation, and
after the transparent gateway passes validation.

## Acceptance Criteria

The deployment is accepted only when all of the following are demonstrated:

1. VMM is healthy and the Debian VM survives a controlled restart.
2. `192.168.15.201` has no duplicate-address conflict and can reach the NAS,
   router, DNS, and Internet.
3. The EasyProxy container starts with zero restart loops and its management API
   reports a healthy runtime node pool.
4. Google succeeds through the explicit proxy at `192.168.15.201:22323`.
5. A physical-LAN test client with no application or system proxy can use
   `192.168.15.201` as its default gateway and reach Google over IPv4 TCP.
6. Local/private destinations remain reachable and do not loop through the
   proxy pool.
7. With all proxy nodes unavailable, the same client remains online through the
   configured DIRECT fallback.
8. Restoring proxy nodes returns eligible TCP traffic to PROXY routing.
9. Stopping EasyProxy removes its transparent capture state without removing
   the VM's base forwarding/NAT state.
10. Restoring the client's previous default gateway immediately bypasses the VM
    and restores the original network path.

## Deferred Work

- Transparent UDP and QUIC proxying.
- IPv6 transparent capture and routing.
- Automated selection between redundant overlay paths.
- Multiple-VM or multiple-NAS gateway failover.
- Retirement of the existing DSM EasyProxy container.
