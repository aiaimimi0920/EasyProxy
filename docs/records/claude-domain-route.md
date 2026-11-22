# Claude domain route (not deployed)

This source feature is disabled by default. It matches destination domains, not processes or proxy identities. All applications using a matching domain through a covered EasyProxy entry receive the same policy.

```yaml
claude_route:
  enabled: true
  include_shared_dependencies: false
  node_tag: "<exact-approved-pool-member-tag>"
  node_fingerprint: "<sha256-of-exact-approved-node-uri>"
```

The tag must be an independently verified GLaDOS member selected at deployment approval time. Source code does not choose a node or infer the provider from a label. A fixed node does not guarantee a fixed public exit IP. Missing/blank tags or missing/malformed fingerprints reject matching requests. The exact URI fingerprint also must match at selection: renamed/replaced or reordered same-name nodes cannot silently move the binding. URI changes require re-verification and approval; do not put the raw URI or credentials in reports. No production configuration was modified.

Covered paths: existing native HTTP/SOCKS pool inbound (without taking over its port), dispatch HTTP/CONNECT, native TUN TCP and UDP. The policy takes precedence over ordinary DIRECT rules, shared selection tiers and per-request node hints. Matching requests only select the exact member; missing, failed, authentication-rejected or protocol-incompatible members cause errors, not another member or DIRECT. TUN has an explicit per-flow no-fallback gate, including a missing pool. Non-matching requests retain existing routing. General and restricted node pools carry the guard; a restricted pool lacking the requested member rejects rather than escaping that pool. Bootstrap detour pools are unchanged.

Domains are based on https://code.claude.com/docs/en/network-config : api.anthropic.com, claude.ai, claude.com, platform.claude.com, sso.claude.com, mcp-proxy.anthropic.com, downloads.claude.ai, bridge.claudeusercontent.com, code.claude.com assets-proxy.anthropic.com and subdomains of frame.claudeusercontent.com and claudemcpcontent.com. Matching is case-insensitive and DNS-label bounded. Shared dependency hosts require the explicit option described below. Arbitrary plugin/MCP/WebFetch destinations and third-party model providers are not included.

This is not a host firewall. Direct connections bypassing EasyProxy, IP-only traffic with no recoverable domain, legacy TPROXY original-IP-only traffic, or encrypted hostname metadata are not covered. No TLS interception is introduced. DNS queries are not pinned by this feature. Current connections are not migrated. It requires a reviewed normal runtime rebuild/restart, not a routing hot-apply. Enabled/tag/fingerprint/shared-dependency-group changes participate in dispatcher topology so the normal reload also rebuilds its immutable policy; no management UI/API toggle is added. Deployment, real node selection and service restart require separate approval. Rollback is restoring the previous reviewed image/config; disabling the feature restores ordinary behavior and therefore removes fail-closed protection.

Earlier protected-client identity prototype is preserved but is not used by this domain route.

## Configuration and verification workflow

The checked-in examples (`service/base/config.example.yaml` and
`deploy/service/base/config.template.yaml`) expose the three settings with
`enabled: false`. They are examples, not the deployed runtime configuration.
Existing installations are not changed by editing either example.

1. Build the source containing this feature; do not assume an older deployed
   binary recognizes `claude_route`.
2. Identify one exact pool member from the approved subscription. Bind both its
   generated tag and `config.NodeFingerprint` of its exact URI (SHA-256 after
   trimming surrounding whitespace, encoded as lowercase hexadecimal). Do not
   publish that URI or subscription credentials. A friendly node name alone is
   not sufficient to identify a stable subscription entry.
3. After deployment approval, back up the selected runtime YAML and configure
   the binding there. Use the normal rebuild/reload procedure. Do not enable an
   incomplete binding: matched traffic will deliberately fail closed.
4. Verify successful matching traffic and a deliberately unavailable binding
   in a non-production test environment before enabling it for normal use.
   Nonmatching traffic must retain its original routing. Verify separately
   that the actual client uses a covered EasyProxy listener.
5. Subscription changes can invalidate a tag or URI. This intentionally blocks
   matching traffic until the binding is reviewed again; it is not a reason
   to enable automatic fallback.

Source regression command (from `service/base`):

```text
go test ./internal/config ./internal/outbound/gatewayroute ./internal/outbound/pool ./internal/dispatch ./internal/builder ./internal/app
```

The tests cover destination boundaries, disabled defaults, YAML load/clone/save
round trips, binding changes, HTTP/CONNECT failures, TCP/UDP failure isolation,
missing/replaced/auth-rejected members, ordinary traffic and builder/reload
wiring. They do not constitute NAS deployment or real-network verification.

Per-node multi-port listeners also carry the guard. Because their pool contains
only that port's member, a Claude request on a different member's port fails
rather than switching to the configured member outside its pool. Regional and
other restricted pools follow the same rule. Use the main pool entry when the
pinned member should be reachable without depending on a restricted pool.

## Domain group review (2026-11-15)

This is one domain group with one node binding, not a different route per host.
`include_shared_dependencies: true` extends that SAME binding to these exact
hosts: `storage.googleapis.com`, `registry.npmjs.org`, `github.com`,
`raw.githubusercontent.com`, `http-intake.logs.us5.datadoghq.com`,
`browser-intake-us5-datadoghq.com`, `formulae.brew.sh`.
Default false avoids changing other applications' traffic to shared services.
When enabled, those other applications also use the pinned route and fail when
that node fails. NAS destination matching cannot identify a remote Linux process
or distinguish Claude's object path inside encrypted shared-host traffic.

The [official network requirements](https://code.claude.com/docs/en/network-config#network-access-requirements)
list core authentication/API, downloads, connectors, artifacts, shared package
and metadata services, optional telemetry and installation-specific services.
The implementation uses exact hosts and the documented content subdomain
patterns rather than `*.googleapis.com`, `*.github.com` or `*.anthropic.com`.
New hosts are reviewed additions with boundary tests, not unrestricted user
patterns. Arbitrary extensions are deliberately not offered in this version.
Gerrit per-checkout hosts, third-party artifact fonts/libraries, user-selected
MCP/WebFetch destinations and alternate model gateways remain outside the group.
A domain group cannot exhaustively identify all traffic a coding agent generates.

### What was borrowed from IP Guard

Reviewed [ip-guard](https://github.com/chxcodepro/ip-guard/tree/801143b76f19848e28ceffd923b31f5e0bba1a10)
without running or copying its code. Its enhanced route is a local process-bound
proxy channel, not a complete Claude-domain catalog. `proxy-enhancement.ts`
forwards admitted business requests through its forced endpoint;
`claude.ai` and `myip.ipip.net` are restricted probe destinations, not the full
business domain group. `ClaudeCodeScope.cs` classifies API/model configuration;
the browser extension has separate claude.ai rules. See
[proxy handling](https://github.com/chxcodepro/ip-guard/blob/801143b76f19848e28ceffd923b31f5e0bba1a10/apps/desktop/src/main/proxy-enhancement.ts)
and [scope classification](https://github.com/chxcodepro/ip-guard/blob/801143b76f19848e28ceffd923b31f5e0bba1a10/apps/desktop/native/ClaudeCodeScope.cs).

We keep the user's simpler NAS domain-group design. No Windows driver, process
interception, extra proxy core or periodic IPv4 allowlist enforcement is added.
Fixed-node failure isolation is therefore not equivalent to IP-change detection.

Regression tests enumerate every default/shared hostname, reject lookalike
suffixes, check disabled/opt-in behavior and verify all selected hosts resolve to
the SAME tag and fingerprint despite conflicting ordinary node hints. Persistence
and reload tests include the shared-group option.
