# Contributing

All external contributions go to this repository.

Do not look for separate repositories for `EasyProxy`, `MiSub`, `aggregator`,
or `ech-workers`. The public contribution path is always this monorepo.

## Where To Change Code

- `service/base`
  - main EasyProxy runtime
- `upstreams/misub`
  - upstream-tracked shared source registry and manifest center
- `upstreams/aggregator`
  - upstream-tracked fallback artifact producer
- `upstreams/ech-workers`
  - upstream-tracked local ECH helper runtime
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
4. If you change `upstreams/aggregator` or `upstreams/ech-workers`, explain
   whether the change is:
   - an upstream sync import
   - a local patch carried on top of upstream
   - a documentation-only adjustment
5. If you change `upstreams/misub`, call out whether the change is an
   upstream-derived sync/import or a local patch carried on top of the
   maintained fork.

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
npm run build
```

For `upstreams/aggregator` and `upstreams/ech-workers`:

- follow the upstream-native validation flow documented in their own READMEs
- keep local patches narrow and easy to identify

For deployment changes:

- update the corresponding notes under `deploy/`
- update the corresponding private operator notes under `AIRead` outside this
  repository when needed
- if the change affects release or publish flow, check `docs/release-checklist.md`

## Commit Style

Small, focused pull requests are preferred. If a change spans multiple modules,
call that out explicitly in the PR summary.
