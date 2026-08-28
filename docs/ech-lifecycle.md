# ECH Worker lifecycle

This document is the operator contract for the first-party Cloudflare Worker,
the forked Go helper, MiSub connector state, and EasyProxy validation.

## Stable identity and ordinary updates

`workers/ech-workers-cloudflare/wrangler.jsonc` owns the Worker name. Before an
update, `scripts/verify-cloudflare-worker-identity.py` performs an exact
Cloudflare API lookup. Update fails when that name is absent; it never deploys a
replacement Worker under a guessed name. Bootstrap may create the configured
name.

An ordinary `.github/workflows/deploy-cloudflare.yml` run:

1. requires the existing `ECH_TOKEN` secret;
2. does not generate or rotate a token;
3. deploys to the tracked Worker name and existing public URL;
4. verifies the exact Cloudflare identity, HTTP banner, current token, invalid
   token rejection, and a remote TCP request through the Worker;
5. synchronizes MiSub while preserving managed `server_ip` values by default.

Use `--drop-server-ips` with `scripts/sync-misub-ech-profile.py` only when the
operator intentionally wants to discard those values.

## Safe token rotation

Token rotation is separate from deployment. Run **Rotate ECH Token** only after
configuring the protected `easyproxy-ech-rotation` GitHub Environment.

Required rotation secrets:

| Name | Purpose |
| --- | --- |
| `ECH_TOKEN` | Current canonical token. |
| `ECH_TOKEN_NEXT` | New token; must differ from the current token. |
| `EASYPROXY_REPOSITORY_ADMIN_TOKEN` | Fine-grained token used only to replace the repository `ECH_TOKEN` secret; requires repository Secrets write permission. |
| `EASYPROXY_MANAGEMENT_PASSWORD` | Authenticates the dedicated EasyProxy validation instance. |
| `MISUB_ADMIN_PASSWORD` | Updates and rolls back MiSub connector state. |
| `MISUB_MANIFEST_TOKEN` | Verifies candidate and final MiSub manifests. |
| Cloudflare credentials | Updates Worker code and secrets for the exact account/name. |

Required variables:

| Name | Purpose |
| --- | --- |
| `EASYPROXY_ECH_WORKER_PUBLIC_URL` | Existing public Worker URL. |
| `EASYPROXY_MISUB_PUBLIC_URL` | MiSub control-plane URL. |
| `EASYPROXY_ROTATION_BASE_URL` | Management URL for a dedicated EasyProxy validator reachable from the selected runner. |
| `EASYPROXY_ROTATION_PROXY_URL` | HTTP proxy URL for the same validation instance. |
| `EASYPROXY_ROTATION_RUNNER` | Optional JSON runner label array; defaults to `["ubuntu-latest"]`. |

The validation EasyProxy instance must use the target MiSub profile and must be
isolated so that its validation proxy traffic cannot silently fall back to an
unrelated connector. A self-hosted runner is appropriate when this instance is
reachable only inside a LAN.

The workflow performs these guarded stages:

1. confirm the exact Worker name and save the old token only in a private runner
   temporary file;
2. publish dual-token-capable code while the old token is still current;
3. set the new current token and a time-limited previous-token binding;
4. prove old and new tokens through Worker TCP tunnels and through the Go helper
   using both HTTP and SOCKS5;
5. add a MiSub candidate connector without deleting the old connector;
6. wait for the dedicated EasyProxy instance to load the candidate and complete
   a real proxy request;
7. switch MiSub to the canonical connector and persist `ECH_TOKEN_NEXT` as the
   repository `ECH_TOKEN` secret;
8. expire, verify rejection of, and remove the old Worker token.

Any failure after mutation triggers a best-effort rollback of Worker current
token, MiSub connector state, and the repository canonical secret. A failed
rollback keeps the workflow failed and requires operator intervention; it is
never reported as success.

## Worker token semantics

The Worker always requires `ECH_TOKEN`. During rotation it also accepts
`ECH_TOKEN_PREVIOUS` only when `ECH_TOKEN_PREVIOUS_EXPIRES_AT` parses as a
strictly future ISO timestamp or Unix epoch. Missing, malformed, or expired
previous-token metadata is fail-closed. Arbitrary invalid tokens remain rejected.

The overlap maximum is 240 minutes. The workflow first expires the previous
token, proves it is rejected, and then deletes both temporary bindings.

## Go helper release

`upstreams/ech-workers` is the public fork submodule. Its tag workflow uses the
Go version declared in `go.mod`, runs all packages before cross-compilation,
publishes Linux/macOS/Windows binaries, and includes `SHA256SUMS`.

The helper reads authentication from `ECH_TOKEN` when `-token` is omitted. Use
the environment variable in automation so the token is absent from process
arguments. `scripts/verify-ech-helper-e2e.py` starts the helper and proves both
HTTP and SOCKS5 traffic through a real Worker tunnel.

## Live acceptance evidence

Local unit tests and workflow validation do not prove a production rotation.
Phase acceptance requires one protected workflow run against real Cloudflare,
MiSub, and a dedicated EasyProxy validation instance. Retain the workflow URL,
Worker name, root and submodule commits, overlap/revocation results, and rollback
result without retaining secret values.
