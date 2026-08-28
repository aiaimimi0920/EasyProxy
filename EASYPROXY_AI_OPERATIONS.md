# EasyProxy AI Operations Manual

> This document is the operator contract for another AI, automation agent, or
> service integrating with EasyProxy. Read the repository configuration and the
> target host state before changing anything. Never invent credentials, image
> tags, source URLs, or deployment addresses.

## 1. Product Model

EasyProxy is a multi-surface proxy runtime, not only a static subscription
converter.

| Surface | Responsibility | Source of truth |
| --- | --- | --- |
| service/base | Go runtime, proxy listener, routing, node pool, management API, SQLite, connectors | service/base/internal/ |
| upstreams/misub | Manifest management, source probing, aggregation, Vue UI, Cloudflare Functions | upstreams/misub/ |
| upstreams/aggregator | Upstream subscription collection and renewal workflows | upstreams/aggregator/ |
| upstreams/ech-workers | ECH worker runtime used through connector sources | upstreams/ech-workers/ |
| workers/ech-workers-cloudflare | Cloudflare worker deployment surface | workers/ech-workers-cloudflare/ |
| deploy/ and scripts/ | Lifecycle adapters, Docker/GHCR operations, gateway assets, release checks | deploy/, scripts/ |

`topology.yaml` is the non-secret deployment contract. It contains environment
variable names, never resolved credentials. `deploy/service/base/config.yaml`
is the persistent local runtime authority; ordinary deploys and topology updates
must not overwrite it. There is no root renderer or second config writer.

## 2. Deployment Modes

### 2.1 Local Docker or GHCR deployment

This is the normal operator path for service/base.

Prerequisites:

- PowerShell and Docker Engine/Desktop with Docker Compose.
- A real GHCR owner and release tag, or a complete image reference.
- A validated `topology.yaml` and an initialized service runtime config.

Initialize both independent contracts:

    powershell -ExecutionPolicy Bypass -File .\scripts\init-topology.ps1
    powershell -ExecutionPolicy Bypass -File .\scripts\init-runtime-config.ps1

Resolve each environment variable name referenced by topology in the process or
protected platform secret store. Runtime-only credentials remain in the ignored
service config. For example:

    $env:CLOUDFLARE_ACCOUNT_ID = '<account-id>'
    $env:CLOUDFLARE_API_TOKEN = '<deployment-token>'
    $env:MISUB_MANIFEST_TOKEN = '<machine-token>'
    $env:ECH_TOKEN = '<connector-token>'

Add connector credentials only for enabled connectors. Keep passwords, API keys,
access tokens, R2 keys, Cloudflare tokens, and ECH tokens in a secret store or
environment-backed config. Never commit them.

Deploy a published GHCR release:

    powershell -ExecutionPolicy Bypass -File .\scripts\deploy-easyproxy.ps1 -TopologyPath .\topology.yaml -FromGhcr -GhcrOwner <owner> -ReleaseTag <release-tag>

Equivalent root wrapper:

    powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project easyproxy-ghcr -TopologyPath .\topology.yaml -ReleaseTag <release-tag>

Pin a complete image directly:

    powershell -ExecutionPolicy Bypass -File .\scripts\deploy-easyproxy.ps1 -FromGhcr -Image ghcr.io/<owner>/easy-proxy-monorepo-service:<release-tag>

The root deploy script initializes the runtime config only when it is absent,
ensures the runtime Docker network, pulls the image unless -SkipPull is used,
writes Compose inputs, replaces the same-name runtime container, and starts it
through Compose. The lower-level implementation is
deploy/service/base/scripts/deploy-ghcr-runtime.ps1.

For source-less hosts, use -ImportCode or -BootstrapFile. Those inputs are
mutually exclusive. Do not enable remote bootstrap sync unless the bootstrap/R2
service is intended to own later config replacements.

### 2.2 Service-only development run

For a local checkout and a rendered config:

    Set-Location service/base
    go mod download
    go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" -o easy-proxy .\cmd\easy_proxies
    .\easy-proxy.exe --config .\config.yaml

The embedded management UI is normally served at
http://127.0.0.1:29888/.

### 2.3 Debian/NAS gateway deployment

The approved topology separates the Synology DSM host from the Debian VM:

| Host | Role |
| --- | --- |
| 192.168.15.200 | Synology DSM host; may run an overlay relay |
| 192.168.15.201 | Debian 12 VM; validated EasyProxy gateway runtime |

Repository bootstrap path:

    sudo ./deploy/gateway/debian/bootstrap-gateway.sh
    sudo mkdir -p /opt/easyproxy-gateway/config /opt/easyproxy-gateway/data /opt/easyproxy-gateway/compose
    sudo cp ./deploy/gateway/debian/docker-compose.yaml /opt/easyproxy-gateway/compose/
    sudo cp ./deploy/gateway/debian/.env.example /opt/easyproxy-gateway/compose/.env
    cd /opt/easyproxy-gateway/compose
    sudo docker compose up -d

Set EASY_PROXY_IMAGE in the target .env to a trusted image. An example image
tag in a README is not proof that a public tag still exists.

Gateway prerequisites are host networking, CAP_NET_ADMIN, CAP_NET_RAW,
iproute2, nftables, policy routing, and kernel TPROXY support. Gateway
containers must use EASY_PROXY_RUN_AS_ROOT=1; ordinary proxy containers should
remain unprivileged. Do not expose 22323, 29888, or the transparent listener to
WAN or guest networks.

The checked-in Debian asset is a conservative gateway asset. The live validated
.201 instance additionally has /dev/net/tun, CAP_NET_ADMIN, and CAP_NET_RAW.
This is a property of that deployed Compose/runtime input, not a guarantee that
every checkout has a complete TUN deployment. Verify the actual container
device/capabilities before claiming TUN support.

Transparent gateway rollback:

1. Set gateway.enabled: false and reload, or stop the gateway container.
2. Confirm GET /api/gateway/status returns applied: false.
3. Inspect ip rule, ip route show table 100, and nft list table inet easyproxy_gateway.
4. Restore the client route/default gateway.

Never delete another application's nftables table or policy rule. The current
transparent-gateway documentation only claims Phase-1 TCP behavior. Do not claim
complete UDP, QUIC, WebRTC-like, IPv6-capture, or DNS interception support just
because an API payload contains udp_enabled or DNS fields.

## 3. Configuration Contract

The deployment schema is `topology.schema.json`; the service schema is
`service/base/config.example.yaml`; the one-time runtime template is
`deploy/service/base/config.template.yaml`.

| Path | Meaning |
| --- | --- |
| mode | Runtime mode; pool mode is safest for Local Server |
| listener | Explicit HTTP/CONNECT/mixed proxy address, protocol, and optional credentials |
| multi_port | Optional additional protocol ports; avoid with Local Server |
| pool | Outbound selection and health pool settings |
| management | Management listener, probe targets, interval, password |
| routing | Smart routing, rule files, providers, strategy, optional custom listener |
| local_server | Device/profile-aware local server; requires pool + mixed listener topology |
| gateway | Transparent gateway settings and trusted ingress/capture policy |
| source_sync | Manifest URL/token, refresh interval, fallback sources, connector runtime |
| connectors | ECH, ZenProxy, and other runtime connector descriptors |
| subscriptions | Direct subscription URLs |
| nodes / nodes_file | Static node alternatives |

Smart routing has two deployment paths:

- Route A: routing.enabled true and empty routing.listen; dispatch takes over
  the existing listener, normally 22323.
- Route B: set a different routing.listen and publish that port in Compose.
  Editing YAML alone does not publish a host port.

Local Server is deliberately incompatible with legacy multi-port, hybrid, and
extra-listener topologies. Its effective credentials come from
`local_server.auth`. The password is write-only in the UI and must not be
recovered from a read response.

## 4. Using the Proxy

The standard explicit listener is 22323. HTTP forward requests and HTTPS CONNECT
are handled by the dispatch server:

    $proxy = "http://<proxy-user>:<proxy-password>@<host>:22323"
    curl.exe --proxy $proxy https://www.google.com/generate_204 -I --max-time 25

For a SOCKS-capable deployment, use the protocol and credential combination in
the actual rendered config. Do not infer SOCKS availability from a mixed label
alone; perform a real SOCKS handshake against the target release.

The live .201 smoke test passed authenticated HTTP proxy traffic and returned
Google 204. An unauthenticated request correctly returned 407 Proxy
Authentication Required. This proves the listener/upstream path, not universal
health of every node.

Transparent gateway clients must route trusted traffic to the Debian gateway.
The same rule engine and node pool are used for transparent TCP and explicit
proxy traffic. The default failure policy is:

    usable proxy node -> PROXY
    no usable node    -> DIRECT
    gateway stopped   -> normal forwarding after rule cleanup

## 5. Management API

Base URL: http://<management-host>:29888.

GET /api/auth is unauthenticated discovery. It reports whether the runtime uses
the canonical username/password pair. Other API routes use management auth.

Accepted forms are implemented in service/base/internal/monitor/server.go:

    Authorization: <management-password>

The middleware also accepts Authorization: Bearer <session-or-password>, a
valid session_token cookie, or HTTP Basic authentication with the configured
management username/password. Proxy-Authorization is not a management API
credential.

Example:

    $headers = @{ Authorization = "<management-password>" }
    Invoke-RestMethod -Uri "http://<host>:29888/api/source-sync/status" -Headers $headers

### Runtime status and operations

| Method | Path | Purpose |
| --- | --- | --- |
| GET | /api/auth | Discover auth mode; no auth required |
| GET | /api/source-sync/status | Manifest/source health, last sync, fallback state, counts |
| GET | /api/source-sync/source-health | Per-source health details |
| GET | /api/subscription/status | Refresh state, count, last success/error |
| POST | /api/subscription/refresh | Request a subscription refresh |
| GET | /api/routing/status | Routing controller status |
| GET/PUT | /api/routing/config | Read/update routing configuration; revision guarded |
| GET | /api/gateway/status | Gateway/TUN lifecycle and direct/proxy counters |
| POST | /api/reload | Reload runtime configuration |
| GET | /api/nodes | Current runtime node view |
| GET/PUT | /api/settings | Read/update service settings |
| GET | /api/debug | Diagnostic runtime view |
| GET | /api/best-proxy | Select/report the best current proxy |
| GET/POST | /api/export and /api/import | Export/import runtime data |

Node and connector management route families:

    /api/nodes/config
    /api/nodes/config/batch-toggle
    /api/nodes/config/batch-delete
    /api/nodes/config/{id}
    /api/connectors/config
    /api/connectors/config/{id}
    /api/connectors/config/{id}/preferred-ips/refresh
    /api/nodes/probe-all
    /api/nodes/traffic/stream
    /api/nodes/{id}/...

Exact JSON schemas are defined by Go handlers and tests. An AI client should
first GET the relevant resource, preserve unknown fields, and send only known
mutation fields.

### Local Server API

All Local Server routes are authenticated. Mutations use optimistic concurrency.
Send expected_revision in JSON or the corresponding If-Match header. A stale
revision returns HTTP 409:

    {
      "error": "...",
      "current_revision": 7,
      "need_reload": true
    }

| Method | Path | Main fields |
| --- | --- | --- |
| GET | /api/local-server/status | enabled, listen, dispatcher_ready, registry_revision, profile_count, mapping_count, provider_degraded_count, source_ip_warning |
| GET/PUT | /api/local-server/config | Local Server config; PUT is revision guarded |
| GET/PUT | /api/local-server/profiles/shared | PUT body profile, expected_revision |
| GET | /api/local-server/devices | devices array |
| GET/PUT | /api/local-server/devices/{id} | PUT display_name, expected_revision |
| GET/PUT/DELETE | /api/local-server/devices/{id}/profile | Device profile resource |
| PATCH | /api/local-server/devices/{id}/profile/enabled | enabled, expected_revision |
| POST | /api/local-server/devices/{id}/profile/copy-shared | One-time copy of shared profile |
| GET/POST | /api/local-server/ip-mappings | POST cidr, device_id, priority, enabled, expected_revision |
| PUT/DELETE | /api/local-server/ip-mappings/{id} | Mapping fields and optional revision guard |

Mutation responses use:

    {
      "revision": 8,
      "registry_revision": 9,
      "need_reload": false,
      "profile_scope": "device",
      "resource": {}
    }

### Proxy compatibility API

These routes expose a provider/lease abstraction. They are not the explicit
proxy listener.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | /proxy/catalog | Provider types, templates, strategy profiles/groups, default strategy |
| GET | /proxy/snapshot | Instances, bindings, leases, feedback, and stats |
| POST | /proxy/leases/plan | Probe/select a candidate without committing |
| POST | /proxy/leases/checkout | Commit a lease; serialized checkout and initial probe |
| POST | /proxy/leases/report | Report success, latency, failure class, route confidence |
| GET | /proxy/leases/{id} | Read one lease |
| POST | /proxy/leases/{id}/release | Release a lease; returns ok true |
| POST | /proxy/maintenance/run | Expire/clean/refresh compatibility records |

Plan/checkout fields include hostId, providerTypeKey, provisionMode, bindingMode,
strategy selectors, optional preferred instance/template/group, protocol,
ttlMinutes, and metadata. Checkout returns a lease with id, proxyUrl, host,
port, protocol, credential fields, status, expiry, and metadata. Never log or
persist the returned password outside a protected secret store.

Initial probe may still be running. Plan/checkout can return HTTP 503 with
INITIAL_PROXY_PROBE_PENDING; retry with bounded backoff and inspect node/source
status before escalating.

## 6. API Client Rules

1. Discover auth with /api/auth; do not guess the auth form.
2. Use explicit timeouts and bounded retries for refresh, probe, and lease calls.
3. Treat HTTP 409 as stale revision: re-read, merge deliberately, then retry.
4. Treat HTTP 503 INITIAL_PROXY_PROBE_PENDING as pending, not permanent failure.
5. Redact password, token, secret, api_key, proxyUrl, cookies, and auth headers.
6. Do not use Proxy-Authorization for management API calls.
7. Do not equate /api/nodes availability with every node being usable.

## 7. Validation Gates

Run serially from the repository root before committing runtime changes:

    python -m unittest discover -s tests -p "test_*.py" -v
    python -m unittest discover -s upstreams/aggregator/tests -p "test_*.py" -v
    python scripts/check-go-format.py service/base/cmd service/base/internal
    Set-Location service/base
    go test -count=1 ./...
    go vet ./...
    Set-Location frontend
    npm ci
    npm run test
    npm run lint
    npm run build
    Set-Location ../..
    python scripts/validate-release-contract.py
    git diff --check

When MiSub or manifest/Cloudflare behavior changes:

    Set-Location upstreams/misub
    npm ci
    npm run test:run

Gateway read-only validation:

    .\scripts\validate-transparent-gateway.ps1
    sudo ./scripts/validate-transparent-gateway-linux.sh

## 8. Live Reference Checkpoint

Observed read-only on 2026-08-19; this is evidence for that deployment only:

- Debian VM 192.168.15.201, hostname easyproxy-gateway.
- Container easy-proxy-gateway, image
  ghcr.io/aiaimimi0920/easy-proxy-monorepo-service:native-tun-20260819-candidate17.
- Running, unless-stopped, zero restarts, not OOM-killed, host network, CAP_NET_ADMIN,
  CAP_NET_RAW, and /dev/net/tun.
- Explicit listener 0.0.0.0:22323; management 0.0.0.0:29888.
- Management/auth, source-sync, subscription, routing, gateway, local-server, and
  nodes endpoints returned HTTP 200 with configured auth.
- Source sync was healthy, fallback inactive, with one local source, 70 manifest
  sources, and two connector sources.
- Subscription reported five refreshes, 139 nodes, and no last error; 32 nodes
  were marked available in the sampled response.
- Gateway reported applied true; easyproxy0 and policy route table 100 were present.
- Authenticated explicit HTTP proxy traffic to Google generate_204 returned 204.
- Four warning-level canceled outbound dial attempts appeared in the sampled 60-minute
  logs. They were node-level failures while the service remained healthy; re-check
  current logs before declaring an incident.
- NAS DSM host 192.168.15.200 had easyproxy-overlay-relay running in host network
  with no recent log lines; it is not the main .201 runtime.

## 9. Change and Security Boundaries

- Keep legacy repositories read-only; make EasyProxy changes here only.
- Keep GitHub Actions for cloud/publication and root scripts for local deployment.
- Do not restart a live container to validate a code change; use an isolated name,
  port, config path, and data directory.
- Do not commit graphify-out/, .memsearch/, .playwright-*, temporary archives,
  rendered secret-bearing configs, .env, .dev.vars, or import codes.
- Before pushing, inspect status, staged diff, remote divergence, and diff check.
  Rebase onto current origin/main instead of overwriting remote commits.

## Source Anchors

- Root deployment: README.md, scripts/deploy-subproject.ps1, and
  scripts/deploy-easyproxy.ps1.
- Deployment/runtime schemas: topology.schema.json,
  service/base/config.example.yaml, deploy/service/base/config.template.yaml.
- Management routes: service/base/internal/monitor/server.go:212-255.
- Auth: service/base/internal/monitor/server.go:1875-1922.
- Local Server schema/routes: service/base/internal/monitor/local_server.go:32-146.
- Dispatch protocol: service/base/internal/dispatch/server.go:53-55,280-304.
- Gateway contract: docs/transparent-gateway.md:8-92.
- Gateway assets: deploy/gateway/debian/README.md and docker-compose.yaml.
- CI gates: .github/workflows/validate.yml and
  .github/workflows/reusable-validate.yml.
