# Debian VM Transparent Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (or superpowers:subagent-driven-development) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Install a Debian 12 VM on the Synology DS920+, run the approved EasyProxy Docker image with Linux TPROXY support, and validate it as an isolated IPv4 TCP transparent gateway at `192.168.15.201`.

**Architecture:** Synology Virtual Machine Manager provides one bridged Debian 12 VM on `ovs_bond0`. Docker runs EasyProxy in host networking with `NET_ADMIN` and `NET_RAW`; a separate Debian nftables service owns forwarding/NAT while EasyProxy owns its `easyproxy_gateway` policy-routing table. The existing DSM EasyProxy at `192.168.15.200` remains untouched until every acceptance test passes.

**Tech Stack:** Synology DSM 7.2, Virtual Machine Manager 2.7.0-12229, Debian 12 amd64, Docker Engine, Docker Compose, nftables, iproute2, SSH, EasyProxy image `easyproxy/easy-proxy:transparent-20260722-5dece9d`.

---

### Task 1: Record the NAS baseline and install Virtual Machine Manager

**Files:**
- No repository files modified.

- [ ] **Step 1: Capture the pre-install state**

Run on the NAS through the existing SSH account:

```sh
uname -a
/usr/syno/bin/synopkg status Virtualization
free -m
df -h /volume1
ip -br addr show ovs_bond0
/usr/local/bin/docker ps --format '{{.Names}}|{{.Image}}|{{.Status}}'
```

Expected: DSM remains `7.2-64570`, `Virtualization` is non-installed, volume 1
has at least 32 GiB free, and `easy-proxy-transparent-20260722` is running.

- [ ] **Step 2: Install the official VMM package without stopping Docker**

Run as root on the NAS:

```sh
/usr/syno/bin/synopkg install_from_server Virtualization volume1 mjc
```

Expected: the command downloads the Synology package and exits successfully.
Do not run `synopkg stop ContainerManager`, reboot DSM, or modify existing
Docker networks.

- [ ] **Step 3: Verify VMM and KVM state**

```sh
/usr/syno/bin/synopkg status Virtualization
ls -l /dev/kvm
lsmod | grep -E '^(kvm|kvm_intel|vhost)'
```

Expected: VMM is `running`, `/dev/kvm` exists, and the KVM modules are loaded.

### Task 2: Download and register Debian 12 installation media

**Files:**
- NAS-only media under `/volume1/docker/easyproxy-gateway/iso/`.

- [ ] **Step 1: Create an isolated VM workspace**

```sh
install -d -m 0750 /volume1/docker/easyproxy-gateway/iso
install -d -m 0750 /volume1/docker/easyproxy-gateway/vm
```

- [ ] **Step 2: Download and verify the Debian 12 amd64 netinst ISO**

The verified current oldstable filename is `debian-12.15.0-amd64-netinst.iso`.
Run:

```sh
cd /volume1/docker/easyproxy-gateway/iso
curl --fail --location --retry 3 --remote-name \
  https://cdimage.debian.org/cdimage/archive/latest-oldstable/amd64/iso-cd/debian-12.15.0-amd64-netinst.iso
curl --fail --location --retry 3 --remote-name \
  https://cdimage.debian.org/cdimage/archive/latest-oldstable/amd64/iso-cd/SHA256SUMS
grep 'debian-12.15.0-amd64-netinst.iso$' SHA256SUMS | sha256sum --check -
```

Expected: `debian-12.15.0-amd64-netinst.iso: OK`. Do not use a Debian 13 image
for this deployment.

- [ ] **Step 3: Register the ISO in VMM**

In DSM, open **Virtual Machine Manager -> Image -> ISO File -> Add** and select
the verified ISO under `/volume1/docker/easyproxy-gateway/iso/`.

Expected: the ISO appears as an available amd64 installation image with a
successful import status.

### Task 3: Create and install the Debian gateway VM

**Files:**
- VM storage on `/volume1` managed by VMM.

- [ ] **Step 1: Create the VM with the approved resource profile**

In **Virtual Machine Manager -> Virtual Machine -> Create**, set:

```text
Name:       easyproxy-gateway
CPU:        2 vCPU
Memory:     4096 MiB
Disk:       32 GiB, dynamically allocated, /volume1
Firmware:   VMM default supported by Debian 12
Network:    ovs_bond0 bridged adapter
```

Attach the registered Debian ISO as the virtual CD-ROM and enable automatic
start after the host starts. Do not attach the VM to a Docker bridge.

- [ ] **Step 2: Install Debian with a temporary local administrator**

Use the VMM console and choose the standard Debian installer. Configure:

```text
Hostname:       easyproxy-gateway
Domain:         lan
Primary user:   mjc
Network:        192.168.15.201/24
Gateway:        192.168.15.1
DNS:            192.168.15.1
Partitioning:   guided, use entire 32 GiB virtual disk
Software:       SSH server and standard system utilities
```

Use the already supplied operator password only as the temporary installer
password. Do not install a desktop environment. Reboot from the virtual disk
after installation and eject the ISO.

- [ ] **Step 3: Verify the VM network before installing Docker**

From Windows:

```powershell
Test-NetConnection 192.168.15.201 -Port 22
ssh mjc@192.168.15.201 'ip -br addr; ip route; ping -c 2 192.168.15.1'
```

Expected: SSH succeeds, the VM owns only `192.168.15.201/24`, the default route
uses `192.168.15.1`, and the router responds.

### Task 4: Bootstrap Debian forwarding and Docker

**Files:**
- Create: `deploy/gateway/debian/bootstrap-gateway.sh`
- Create: `deploy/gateway/debian/easyproxy-forwarding.nft`

- [ ] **Step 1: Write focused deployment-asset tests**

Add tests that require the bootstrap script to contain strict shell settings,
the three approved sysctls, Docker package installation, and nftables enablement.
Require the nftables file to create `easyproxy_forwarding`, allow established
forwarding, accept trusted IPv4 sources, and masquerade forwarded traffic. The
test must reject any deployment asset that creates or deletes
`easyproxy_gateway`.

Create `tests/test_debian_gateway_assets.py` with:

```python
from pathlib import Path


ROOT = Path(__file__).parents[1]
ASSET_ROOT = ROOT / "deploy" / "gateway" / "debian"


def test_bootstrap_has_required_host_prerequisites():
    text = (ASSET_ROOT / "bootstrap-gateway.sh").read_text(encoding="utf-8")
    assert "set -euo pipefail" in text
    assert "net.ipv4.ip_forward=1" in text
    assert "net.ipv4.conf.all.send_redirects=0" in text
    assert "net.ipv4.conf.default.send_redirects=0" in text
    assert "docker-ce" in text
    assert "nftables" in text
    assert "easyproxy_gateway" not in text


def test_forwarding_rules_are_separate_from_easyproxy_capture():
    text = (ASSET_ROOT / "easyproxy-forwarding.nft").read_text(encoding="utf-8")
    assert "table inet easyproxy_forwarding" in text
    assert "ct state established,related accept" in text
    assert "ip saddr 192.168.15.0/24 accept" in text
    assert "masquerade" in text
    assert "easyproxy_gateway" not in text


def test_compose_uses_host_network_and_only_gateway_capabilities():
    text = (ASSET_ROOT / "docker-compose.yaml").read_text(encoding="utf-8")
    assert "network_mode: host" in text
    assert "NET_ADMIN" in text
    assert "NET_RAW" in text
    assert "EASY_PROXY_RUN_AS_ROOT" in text
    assert "ports:" not in text
    assert "context:" not in text
```

- [ ] **Step 2: Run the tests and verify they fail**

```powershell
python -m pytest tests/test_debian_gateway_assets.py -q
```

Expected: FAIL because the Debian gateway assets do not exist yet.

- [ ] **Step 3: Implement the Debian bootstrap**

The bootstrap script installs `ca-certificates`, `curl`, `gnupg`, `nftables`,
`iproute2`, `conntrack`, and Docker Engine from Docker's Debian repository. It
writes `/etc/sysctl.d/99-easyproxy-gateway.conf` containing:

```text
net.ipv4.ip_forward=1
net.ipv4.conf.all.send_redirects=0
net.ipv4.conf.default.send_redirects=0
```

It installs the dedicated nftables file under
`/etc/nftables.d/easyproxy-forwarding.nft`, includes it from
`/etc/nftables.conf`, enables `nftables.service`, and verifies:

```sh
command -v docker
docker compose version
command -v nft
command -v ip
test "$(sysctl -n net.ipv4.ip_forward)" = 1
```

Implement `deploy/gateway/debian/bootstrap-gateway.sh` as:

```bash
#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl gnupg nftables iproute2 conntrack python3-yaml

install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg \
  | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
chmod 0644 /etc/apt/keyrings/docker.gpg
. /etc/os-release
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian %s stable\n' \
  "$(dpkg --print-architecture)" "$VERSION_CODENAME" \
  > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

install -d -m 0755 /etc/nftables.d
install -m 0644 "$(dirname "$0")/easyproxy-forwarding.nft" \
  /etc/nftables.d/easyproxy-forwarding.nft
cat > /etc/sysctl.d/99-easyproxy-gateway.conf <<'EOF'
net.ipv4.ip_forward=1
net.ipv4.conf.all.send_redirects=0
net.ipv4.conf.default.send_redirects=0
EOF
sysctl --system
printf 'include "/etc/nftables.d/*.nft"\n' > /etc/nftables.conf
nft -c -f /etc/nftables.conf
systemctl enable --now nftables docker
nft -f /etc/nftables.conf

if id mjc >/dev/null 2>&1; then
  usermod -aG docker mjc
fi

command -v docker
docker compose version
command -v nft
command -v ip
test "$(sysctl -n net.ipv4.ip_forward)" = 1
```

Implement `deploy/gateway/debian/easyproxy-forwarding.nft` as:

```nft
table inet easyproxy_forwarding {
  chain forward {
    type filter hook forward priority filter; policy drop;
    ct state established,related accept
    ip saddr 192.168.15.0/24 accept
  }

  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr 192.168.15.0/24 oifname != "lo" masquerade
  }
}
```

- [ ] **Step 4: Run the asset tests and shell syntax check**

```powershell
python -m pytest tests/test_debian_gateway_assets.py -q
bash -n deploy/gateway/debian/bootstrap-gateway.sh
```

Expected: PASS.

- [ ] **Step 5: Copy and run bootstrap inside the VM**

```sh
sudo install -m 0755 bootstrap-gateway.sh /usr/local/sbin/bootstrap-easyproxy-gateway
sudo install -m 0644 easyproxy-forwarding.nft /etc/nftables.d/easyproxy-forwarding.nft
sudo /usr/local/sbin/bootstrap-easyproxy-gateway
sudo nft list table inet easyproxy_forwarding
sysctl net.ipv4.ip_forward net.ipv4.conf.all.send_redirects
docker version --format '{{.Server.Version}}'
docker compose version
```

Expected: Docker, nftables, iproute2, and forwarding are available while the
DSM EasyProxy container remains unchanged.

### Task 5: Deploy the EasyProxy image into the VM

**Files:**
- Create: `deploy/gateway/debian/docker-compose.yaml`
- Create: `deploy/gateway/debian/.env.example`
- Create: `deploy/gateway/debian/README.md`

- [ ] **Step 1: Extend the asset tests for Compose safety**

Require the Compose file to use host networking, add only `NET_ADMIN` and
`NET_RAW`, set `EASY_PROXY_RUN_AS_ROOT=1`, mount a read-only runtime config, use
a persistent data directory, and avoid host port publishing and source-code
bind mounts.

- [ ] **Step 2: Implement the VM Compose file**

Use this service contract:

```yaml
services:
  easy-proxy-gateway:
    image: ${EASY_PROXY_IMAGE:-easyproxy/easy-proxy:transparent-20260722-5dece9d}
    container_name: easy-proxy-gateway
    restart: unless-stopped
    network_mode: host
    cap_add:
      - NET_ADMIN
      - NET_RAW
    environment:
      EASY_PROXY_RUN_AS_ROOT: "1"
    volumes:
      - /opt/easyproxy-gateway/config/config.yaml:/etc/easy-proxy/config.yaml:ro
      - /opt/easyproxy-gateway/data:/var/lib/easy-proxy
```

- [ ] **Step 3: Prepare the VM runtime directories**

```sh
sudo install -d -o root -g root -m 0750 /opt/easyproxy-gateway/config
sudo install -d -o root -g root -m 0750 /opt/easyproxy-gateway/data
sudo install -d -o root -g root -m 0750 /opt/easyproxy-gateway/compose
```

- [ ] **Step 4: Transfer the exact validated image and runtime config**

Inspect the active DSM container bind mounts, copy its active runtime config,
and export the running image without stopping it:

```sh
/usr/local/bin/docker save \
  --output /volume1/docker/easyproxy-gateway/easyproxy-transparent-20260722.tar \
  easyproxy/easy-proxy:transparent-20260722-5dece9d
```

Copy the image tar and active config to the Debian VM. Import the image with:

```sh
docker load --input /opt/easyproxy-gateway/easyproxy-transparent-20260722.tar
```

Management password and node source values remain runtime-only and are not
committed.

- [ ] **Step 5: Start the explicit proxy only**

Keep `gateway.enabled: false`, then run:

```sh
cd /opt/easyproxy-gateway/compose
docker compose up -d
docker ps --filter name=easy-proxy-gateway
curl -fsS -H "Authorization: Bearer ${EASY_PROXY_MANAGEMENT_TOKEN:?set token}" \
  http://127.0.0.1:29888/api/status
```

Expected: the container has zero restarts and the management API responds.

- [ ] **Step 6: Verify Google through the explicit VM proxy**

```powershell
curl.exe --proxy http://192.168.15.201:22323 https://www.google.com/generate_204 -I
```

Expected: HTTP `204` or another successful Google response.

### Task 6: Enable IPv4 TCP transparent capture

**Files:**
- Modify: `/opt/easyproxy-gateway/config/config.yaml` on the VM only.

- [ ] **Step 1: Discover the Debian ingress interface**

```sh
ip -br addr
ip route get 192.168.15.1
```

Use the interface shown by the route as the only physical trusted ingress. Do
not guess `eth0` or add an overlay-specific name.

- [ ] **Step 2: Apply the gateway configuration**

Use the structured YAML parser installed by the bootstrap. The interface is
derived from the active default route and never guessed:

```sh
LAN_IF="$(ip route get 192.168.15.1 | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "dev") { print $(i + 1); exit } }')"
test -n "$LAN_IF"
sudo LAN_IF="$LAN_IF" python3 - <<'PY'
import os
from pathlib import Path

import yaml

path = Path("/opt/easyproxy-gateway/config/config.yaml")
config = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
config["gateway"] = {
    "enabled": True,
    "mode": "transparent",
    "listen": "0.0.0.0:15001",
    "ingress": {
        "interfaces": [os.environ["LAN_IF"]],
        "interface_patterns": [],
        "trusted_cidrs": ["192.168.15.0/24"],
    },
    "capture": {
        "tcp": "tproxy",
        "udp": "disabled",
        "preserve_original_destination": True,
    },
    "routing": {
        "final_policy": "PROXY",
        "no_available_proxy_policy": "DIRECT",
    },
    "dns": {"enabled": False},
}
path.write_text(yaml.safe_dump(config, sort_keys=False), encoding="utf-8")
PY
```

- [ ] **Step 3: Restart only the VM container and inspect gateway state**

```sh
cd /opt/easyproxy-gateway/compose
docker compose restart easy-proxy-gateway
ip rule show
ip route show table 100
nft list table inet easyproxy_gateway
curl -fsS -H "Authorization: Bearer ${EASY_PROXY_MANAGEMENT_TOKEN:?set token}" \
  http://127.0.0.1:29888/api/gateway/status
```

Expected: policy route table 100 and `easyproxy_gateway` exist; the API reports
the listener applied with no lifecycle error.

### Task 7: Run isolated client acceptance tests

**Files:**
- No repository files modified.

- [ ] **Step 1: Capture the test client's original route**

Record its current default gateway and IPv4/IPv6 state. Do not change the LAN
router or any other client.

- [ ] **Step 2: Switch one IPv4-only test client to `192.168.15.201`**

Leave application and system proxy settings empty. Disable IPv6 for the test
interface so it cannot bypass the IPv4 gateway.

- [ ] **Step 3: Verify transparent Google access**

```powershell
curl.exe -4 -I https://www.google.com/generate_204
curl.exe -4 -I https://www.google.com/
```

Expected: successful responses without `--proxy`, and gateway stats record a
TCP connection. A QUIC-only success does not satisfy this check.

- [ ] **Step 4: Verify local bypass and fail-open DIRECT**

```powershell
Test-NetConnection 192.168.15.200 -Port 29888
curl.exe -4 -I http://192.168.15.200:29888/
```

Temporarily make the VM proxy pool unavailable through the authenticated
management API, repeat the Google request, verify that DIRECT keeps the client
online, and restore the pool immediately.

- [ ] **Step 5: Verify cleanup and route rollback**

Stop only the VM EasyProxy container and confirm:

```sh
test "$(sysctl -n net.ipv4.ip_forward)" = 1
nft list table inet easyproxy_forwarding
! nft list table inet easyproxy_gateway
```

Restore the client's original default gateway and confirm the DSM EasyProxy at
`192.168.15.200` is still running and reachable.

### Task 8: Commit deployment assets and verification evidence

**Files:**
- Add: `deploy/gateway/debian/bootstrap-gateway.sh`
- Add: `deploy/gateway/debian/easyproxy-forwarding.nft`
- Add: `deploy/gateway/debian/docker-compose.yaml`
- Add: `deploy/gateway/debian/.env.example`
- Add: `deploy/gateway/debian/README.md`
- Add: `tests/test_debian_gateway_assets.py`

- [ ] **Step 1: Run focused and existing tests**

```powershell
python -m pytest tests/test_debian_gateway_assets.py -q
Push-Location service/base
go test ./internal/gateway ./internal/dispatch
Pop-Location
git diff --check
```

Expected: all checks pass.

- [ ] **Step 2: Verify no runtime secrets are staged**

```powershell
git status --short
git diff --cached --name-only
```

Expected: no active `config.yaml`, `.env`, node subscription, management token,
VM private key, ISO, virtual disk, or image tar is staged.

- [ ] **Step 3: Commit only reusable deployment assets**

```powershell
git add deploy/gateway/debian tests/test_debian_gateway_assets.py
git commit -m "feat(deploy): add Debian VM gateway assets"
```
