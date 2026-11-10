# Device selection tiers

The local runtime supports two optional ordered node-selection tiers, followed
by the existing general proxy pool. This is a local policy feature: cloud
manifests, subscriptions, connectors, and their refresh contracts do not change.
There is one global pool, not one pool or container per device.

## Configuration and compatibility

Set `selection_tiers` on a shared or independent forwarding Profile. The shared
Profile persists as `routing.selection_tiers` in the runtime YAML. Independent
Profiles persist in SQLite as complete snapshots with their existing revisions.
The same editor serves both scopes; copying shared creates a one-time snapshot.

```yaml
routing:
  selection_tiers:
    - kind: country
      country: US
    - kind: latency
```

At most two configured tiers are accepted. Supported kinds are `country`,
`latency`, and `stability`. Country requires a two-letter country code, normalized
to uppercase. The other kinds reject a country value. Ordering is preserved.
The implicit final tier cannot be removed or configured. Disabling the first
configured tier in the editor promotes the second to first.

An omitted or empty chain retains legacy selection behavior. The older
`/api/routing/config` alias preserves a saved chain when old clients omit the
field; an explicit empty array clears it. Full Profile PUT remains a complete
replacement. Validation precedes publication and existing revision/CAS conflict
handling still applies.

Existing `node_filter` fields, request-level country filters, strict required
node tags, authentication quarantine, and network/protocol support remain hard
constraints across every tier. Thus the general tier means all otherwise
eligible nodes in the global pool, not permission to escape a hard filter.
For country *preference*, use a country tier rather than a country hard filter.
Country labels use the node server address's existing GeoIP metadata, not a new
measurement or promise about actual public egress geography.

## Choosing and falling through

1. A country tier chooses eligible nodes with that country label.
2. A latency tier chooses the lowest fresh successful probe duration.
3. A stability tier chooses the smallest population standard deviation of
   recent successful probe durations, measured in time units.
4. When a configured tier has no eligible candidate or its bounded dial budget
   is exhausted, selection advances. The final tier uses existing general-pool
   selection.

Each tier may try at most two distinct nodes; failed node tags are excluded
across the whole dial. With two configured tiers there are at most six attempts,
with one at most four. Each setup attempt is bounded to five seconds and the
whole tiered selection to thirty seconds, or the caller's earlier deadline.
This is a bounded availability attempt, not exhaustive proof that every node in
a large country is unusable. It prevents a large failing preferred set from
starving the second and general tiers. Legacy empty chains retain existing
`MaxRetries` behavior. Caller cancellation is not evidence of node failure.

Known unavailable nodes are not preference candidates. Existing mandatory
protocol constraints remain in effect, including available native UDP protocol
requirements. Country and general tiers use `auto`, `stable`, or `session`
within their candidate set. Latency and stability tiers rank by their stated
metric and do not let a sticky binding or an ordinary `pin=`/`X-Proxy-Pin`
override it. A strict required tag remains a hard constraint.

Stable/session bindings are isolated by Profile ID, revision, and tier. A
recovered preferred tier is reconsidered for new flows. Established connections
are not forcibly migrated. Unknown peers have no synthetic session key; NAT
clients with the same observed IP and shared Profile may still share an IP-based
session, as before. Independent Profile IDs or explicit session keys avoid that
ambiguity.

## Probe measurements and cold starts

Latency history is a bounded ring of at most eight successful probe durations.
Stability requires at least three valid samples. Samples are actual probe
measurements, not traffic success counters, health scores, or synthetic copies
of a persisted last latency.

The latest sample must be no older than `max(10 minutes, 2 × health interval)`.
History spans at most `max(10 minutes, 8 × health interval)`. At the default
2-hour health interval these limits are 4 hours and 16 hours; jitter becomes
usable after three successful probes for the same node. A reload preserves
in-memory samples and their timestamps only when the tag and nonempty node URI
are unchanged. Changed or unknown node identity starts cold; persisted last
latency never becomes artificial history. No new background probe frequency
is introduced. Missing, stale, or insufficient
measurements skip that metric tier and allow the next tier/general pool.
Concurrent updates and stale probe generations cannot overwrite current samples.

## Device resolution and entry paths

With Local Server enabled, explicit HTTP, HTTPS CONNECT, SOCKS5, native TUN,
and legacy transparent TCP use the same live Profile registry. Explicit proxy
identity takes precedence, then enabled source IP/CIDR mappings, then shared
fallback. TUN uses the original inbound source address, never the destination or
forwarded HTTP headers. Legacy transparent TCP uses the socket peer. Missing
source metadata selects shared fallback without inventing a device.

TUN applies the selected Profile's rules, enabled state, filters, tier chain,
long-lived policy, and session policy to both TCP and UDP. Disabled Profiles
retain the existing DIRECT behavior. UDP keeps its separate affinity namespace
and native-protocol availability requirements. When Local Server is disabled,
legacy gateway behavior remains in effect, with optional shared tier selection.

A newly started replacement TUN instance admits no TCP or UDP flows until the
matching configuration and Profile publication has committed. Failed candidates
remain closed, and rollback admits flows only after the restored configuration
has been published. This admission check runs before DIRECT rules and fail-open
handling, preventing traffic through a half-published reload.

IP/CIDR mappings are policy selectors, not authentication. Docker/NAT/VPN or
reverse-proxy topology can collapse several clients to one peer. Explicit
`+dev=<id>` is preferred for stable explicit-proxy identification. Transparent
clients need verified source preservation and stable IP mappings; the service
cannot recover identities erased by NAT. Existing shared canonical credentials
do not enforce separate device authorization.

## Direct routing and failure boundary

The three tiers apply only after a routing decision selects PROXY. Existing
DIRECT rules, a disabled Profile, and gateway fail-open settings remain separate
controls. The third tier is a proxy-pool fallback, never DIRECT itself. If the
entire proxy selection fails, explicit proxy requests fail as before; a gateway
with `no_available_proxy_policy: DIRECT` may then use its existing direct
fallback. This feature therefore does not promise strict-US routing or prevent
all direct traffic, IPv6 bypass, or unsupported UDP paths.

## Validation before deployment

Use isolated configuration/data and loopback ports. Cover shared and independent
Profiles concurrently, missing/unknown source, IPv4/IPv6 source identities,
HTTP/CONNECT/SOCKS, TCP/UDP TUN, first/second/general fallthrough, missing metrics,
failed dials, cancellation, hot edits, and stale revisions. A successful unit
suite or WebUI build does not prove an actual NAS TUN deployment. Back up both
runtime configuration and SQLite state before a separately approved deployment.
