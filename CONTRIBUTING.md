# Contributing

The root repository owns product integration, deployment, documentation, and
release validation. The three upstream-derived modules are public submodules.
Changes to their source land in the corresponding maintained fork first; a
second root pull request updates the verified pointer.

Clone with `--recurse-submodules`, or run
`git submodule update --init --recursive` in an existing checkout.

## Where To Change Code

- `service/base`
  - main EasyProxy runtime
- `upstreams/misub`
  - submodule from `aiaimimi0920/MiSub`
- `upstreams/aggregator`
  - submodule from `aiaimimi0920/aggregator`
- `upstreams/ech-workers`
  - submodule from `aiaimimi0920/ech-workers`
- `workers/ech-workers-cloudflare`
  - self-owned Cloudflare-side ECH Worker
- `deploy`
  - deployment templates and helper scripts
- `docs`
  - repository-level architecture, migration, and operator-facing guidance

## Pull Request Expectations

1. Keep changes scoped to the module you are working on.
2. Update documentation when behavior, layout, or deployment flow changes.
3. Never commit secrets, runtime state, generated local config, or private
   deployment files.
4. Never commit source changes from a detached submodule checkout directly in
   the root pull request.
5. For a submodule update, link the fork pull request, official upstream commit
   or comparison, fork validation, root validation, and rollback commit.
6. Update only one upstream pointer per pull request unless a tested
   cross-module contract requires an atomic root change.

## Validation

Minimum regression checks for repository-level changes:

```powershell
python -m unittest discover -s "tests" -p "test_*.py" -v
python -m unittest discover -s "upstreams/aggregator/tests" -p "test_*.py" -v
```

For `service/base`:

```powershell
Set-Location service/base/frontend
npm run build

Set-Location ..
go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" -o easy-proxy ./cmd/easy_proxies
```

For `upstreams/misub`:

```powershell
Set-Location upstreams/misub
npm ci
npm run test:run
npm run build
```

For `upstreams/aggregator` and `upstreams/ech-workers`:

- follow the upstream-native validation flow documented in their own READMEs
- keep local patches narrow and easy to identify
- run `go test ./...` for `upstreams/ech-workers`

For the complete fork-to-root procedure, use `docs/upstream-sync.md` and the
upstream-sync pull request template.

For deployment changes:

- update the corresponding notes under `deploy/`
- update the corresponding private operator notes under `AIRead` outside this
  repository when needed
- if the change affects release or publish flow, check `docs/release-checklist.md`
- if the change affects GitHub-hosted deployment or secret usage, update
  `docs/github-secrets.md`
- if the change affects import-code/bootstrap flow, update
  `docs/service-base-config-distribution.md`

## Commit Style

Small, focused pull requests are preferred. If a change spans multiple modules,
call that out explicitly in the PR summary.
