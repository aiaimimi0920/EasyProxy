# Aggregator Safe Publication

The root `Deploy Aggregator` workflow is the only active publication authority.
The Aggregator fork computes artifacts; first-party scripts isolate, validate,
and promote them in R2. Workflows inside the submodule are references only and
must not be enabled as a second writer.

The workflow also uses a non-cancelling GitHub Actions concurrency group. This
is the supported single-writer boundary. Direct R2 writes and a second enabled
publication workflow are unsupported because they bypass rollback snapshots and
the stable-manifest commit order.

## Object layout

For release ID `<run-id>` and a canonical key such as `subs/effective.txt`:

| Layer | Key | Contract |
| --- | --- | --- |
| Run candidate | `candidate/<run-id>/subs/effective.txt` | Upstream process writes only here. |
| Candidate alias | `candidate/subs/effective.txt` | Latest fully validated candidate. |
| Immutable release | `releases/<run-id>/subs/effective.txt` | Content-addressed by the release manifest hash; never reused. |
| Stable | `subs/effective.txt` | Compatibility path for MiSub and EasyProxy. |
| Stable commit pointer | `manifests/stable.json` | Written last after every stable object passes public verification. |
| Last known good | `last-known-good/subs/effective.txt` | Snapshot of the previous stable object. |

R2 updates one object atomically, but it does not provide a transaction across
multiple object keys. Therefore `manifests/stable.json` is the release commit
point. Fixed stable paths are updated and publicly verified before that
manifest changes; any error restores every attempted key from the pre-promotion
snapshot. Consumers that need a consistent multi-file view must read the stable
manifest and use its immutable `release_key` entries.

## Publication gates

The workflow performs these steps in order:

1. materialize source credentials without committing them;
2. rewrite every Aggregator storage key into `candidate/<run-id>/...`;
3. run the upstream crawler and converters;
4. validate candidate formats and publicly read the core artifacts;
5. run the shared EasyProxy connectivity audit against candidate Clash output;
6. produce `public-effective` and `public-effective-json` from audited nodes;
7. require all configured public candidate artifacts;
8. enforce absolute node/source minimums and relative decline limits;
9. publish and verify immutable release objects;
10. snapshot the previous stable objects, update stable, and write the stable
    manifest last.

Failures before step 10 never touch stable. Failures during step 10 restore the
previous stable bytes and leave the previous manifest as the committed version.
Candidate and immutable release objects may remain for diagnosis.

If a runner is terminated before its exception handler can restore objects, the
next publication reconciles every fixed stable key from the immutable release
named by the still-committed stable manifest before evaluating the new
candidate. Each fixed object write is atomic; readers of several related files
must still use immutable release keys from the manifest to avoid observing an
in-progress compatibility-key sequence.

## Fork configuration

Configure the secrets and variables in
[`github-secrets.md`](github-secrets.md), then enable the `Deploy Aggregator`
schedule with `EASYPROXY_AGGREGATOR_ENABLE_SCHEDULE=true`. The default cron is
six-hourly. A fork may change the cron in the workflow without changing the
Aggregator source fork.

Recommended publication variables:

| Variable | Default | Meaning |
| --- | ---: | --- |
| `EASYPROXY_AGGREGATOR_MIN_STABLE_NODES` | `1` | Absolute effective-node floor. |
| `EASYPROXY_AGGREGATOR_MIN_SOURCE_COUNT` | `1` | Absolute crawled-source floor. |
| `EASYPROXY_AGGREGATOR_MAX_NODE_DROP_RATIO` | `0.60` | Reject a drop greater than 60% from prior stable. |
| `EASYPROXY_AGGREGATOR_MAX_SOURCE_DROP_RATIO` | `0.80` | Reject a drop greater than 80% from prior stable. |

`EASYPROXY_AGGREGATOR_EFFECTIVE_URL` must be exactly
`<EASYPROXY_AGGREGATOR_PUBLIC_BASE_URL>/subs/effective.txt`. Cloudflare/MiSub
deployment verifies that URL against `manifests/stable.json` and the immutable
release copy before synchronizing MiSub. Local EasyProxy uses the same stable
URL only under `source_sync.fallback_subscriptions`, so the fallback remains
available when MiSub is unreachable.

## Recovery

Normal retry creates a new release ID and never reuses an immutable key. To
inspect or manually recover the previous version, use:

- `last-known-good/manifest.json` for the previous committed manifest;
- `last-known-good/<stable-key>` for its compatibility snapshot;
- `last-known-good/releases/<previous-run-id>/<stable-key>` for the immutable
  retained snapshot.

Do not point consumers at `candidate/`. It is an operator inspection layer, not
an availability contract.
