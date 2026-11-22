# Candidate egress validation — 2026-11-14

Status: **not qualified for a fixed single IPv4 exit**. No production node selected or deployment performed.

Candidate: `GLaDOS-US-8cd587e7fead` (anonymized tag digest), Shadowsocks. The approved temporary process instantiated only this one raw outbound, without listeners, a proxy pool, DIRECT fallback, management service, or health callbacks. The node URI was read on the NAS and passed through process stdin; it was not written to files or emitted. HTTPS certificate verification remained enabled; redirects and environment proxies were disabled. Only Cloudflare trace and api.ipify.org were queried.

| UTC start | Round | Target | Family / anonymized exit | HTTP | Request duration |
|---|---:|---|---|---:|---:|
| 19:47:27 | 1 | Cloudflare trace | IPv6 A | 200 | 1111 ms |
| 19:47:28 | 1 | api.ipify.org | IPv4 A | 200 | 1141 ms |
| 19:47:57 | 2 | Cloudflare trace | IPv6 A | 200 | 1110 ms |
| 19:47:58 | 2 | api.ipify.org | IPv4 B | 200 | 1155 ms |
| 19:48:27 | 3 | Cloudflare trace | IPv6 A | 200 | 1065 ms |
| 19:48:28 | 3 | api.ipify.org | IPv4 A | 200 | 1112 ms |

Six of six requests succeeded, over 62.205 seconds with rounds starting 30 seconds apart. IPv4 A and IPv4 B are different observed addresses from the same test service. Comparing IPv4 against IPv6 is not evidence of rotation; the within-api.ipify.org A → B → A change is the reason for rejection. Stable IPv6 within this short sample is not a permanent-IP guarantee, nor a guarantee about Claude's actual chosen address family.

The helper exited with code 0 and no stderr. Temporary NAS executable/directory, desktop build artifact and temporary helper source were removed. No helper process remained. No POST health probe, subscription change, shared policy update, new listener, container, network rule, service restart or production node selection occurred. Existing source implementation and previous user modifications remain unchanged by the live validation.

## Remaining US candidates: 2026-11-14 20:44–20:50 UTC

Six remaining US-marked GLaDOS candidates were tested sequentially, each as a single raw Shadowsocks outbound with its existing obfs-local transport. Three rounds began 30 seconds apart; each round queried only Cloudflare trace and api.ipify.org. Every request created a fresh connection, with redirects disabled and normal HTTPS certificate verification. Node URI stayed in NAS memory/stdin. No listeners, containers, pool, DIRECT fallback, health probe API, production routing changes, or Claude credentials were used.

| Anonymized candidate | Successful HTTP / attempted | ipify IPv4 distinct / successful samples | Cloudflare IPv6 distinct / samples | Duration | Successful-request median |
|---|---:|---:|---:|---:|---:|
| `08bd1dbd1ef7` | 6 / 6 | 1 / 3 | 1 / 3 | 61.071 s | 550 ms |
| `d456f503f93d` | 6 / 6 | 1 / 3 | 1 / 3 | 61.235 s | 574 ms |
| `bf526ec858ad` | 6 / 6 | 1 / 3 | 1 / 3 | 61.044 s | 532 ms |
| `4ef93ea70d22` | 6 / 6 | 1 / 3 | 1 / 3 | 61.298 s | 662 ms |
| `106409344867` | 6 / 6 | 1 / 3 | 1 / 3 | 61.220 s | 599 ms |
| `de40a8909571` | 5 / 6 | 1 / 2 | 1 / 3 | 61.160 s | 569 ms |

Five candidates completed all six requests with unchanged per-service IPs in this short sample. Candidate `de40a8909571` had one ipify request failure; its two successful IPv4 samples matched. This does not prove a permanent, dedicated, or Claude-specific fixed exit. Previously rejected `8cd587e7fead` was not retested.

Initial helper initialization rejected the existing obfs-local option before HTTP probing; this was corrected using the existing transport implementation and is not counted as a node failure. The NAS executable/directory were removed after testing; production config hash remained unchanged. Anonymized machine-readable observations are in `claude-us-candidate-results-2026-11-14.json`. No production candidate was selected or deployed.

## Extended sample: 2026-11-14 22:21–22:31 UTC

Candidate `bf526ec858ad` was tested for 602.117 seconds: 11 rounds spaced 60 seconds apart, with a new isolated helper process and fresh connections on every round. Only Cloudflare trace and api.ipify.org were queried through that one raw Shadowsocks outbound using its existing obfs-local transport. No production gateway/pool, listener or fallback route participated.

- Cloudflare: 11/11 successful HTTP 200 responses; one unchanged IPv6 across all 11 samples.
- ipify: 9/11 successful HTTP 200 responses; one unchanged IPv4 across the 9 successful samples. Rounds 3 and 11 failed before a response was recorded. This probe deliberately omitted error text to prevent credential disclosure; the failure cause is therefore undiagnosed, not proven to be the node itself.
- Total: 20/22 successful requests. No observed IP rotation within either service's successful samples; NOT a fully reliable result and NOT a fixed/dedicated IP guarantee.
- Reconnection scope: disposable test processes/connections only. Neither the production proxy nor the NAS network was restarted/disconnected.
- No Claude login or real Claude request was performed. These results do not establish Claude-specific routing or exit selection.
- NAS temporary executable/directory removed; production configuration SHA256 unchanged. Anonymized observations: `claude-us-long-test-2026-11-14.json`.

## Current candidate recheck: 2026-11-15 07:36–07:39 UTC

**Rejected for fixed-egress use.** Six fresh processes tested candidate
`bf526ec858ad` using URI SHA256
`a2910ce4df86a71b72eab6586e6e31e77a332a62ba132a25227f451af03cdd86`.
All 12 requests succeeded, but both services changed their observed address
between rounds 1 and 2: Cloudflare IPv6 A to B, and ipify IPv4 A to B.
Each then retained B in rounds 2–6. This is within-service, same-family rotation,
not a mistaken comparison of IPv4 and IPv6. The candidate binding remained
unchanged throughout. The old 2026-11-14 observations are not a guarantee.

Each process used only one raw Shadowsocks outbound with its existing obfs-local
options, with no listener, routing pool, alternate outbound or DIRECT fallback.
Requests used normal HTTPS certificate verification, no redirects and no ambient
HTTP proxy. Only Cloudflare trace and api.ipify.org were accessed; no Claude
login or business request was made. Node URI stayed in NAS memory and stdin.
Production configuration remained byte-identical; no production service restart
or routing change occurred. The temporary NAS executable and directory were
removed. Evidence: `claude-isolated-retest-2026-11-15.jsonl`.

The source API labels the node as a generated `proxy_uri` entry. The prior report
called it GLaDOS, but this run has not independently re-established subscription
provenance. Do not treat a US label or a candidate nickname as provider proof.
