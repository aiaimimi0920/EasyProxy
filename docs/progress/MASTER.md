# EasyProxy Monorepo Migration

## Scope

Copy-only migration from the legacy source workspace into the new `EasyProxy`
monorepo.

## Status

| Phase | Status | Notes |
| --- | --- | --- |
| Planning baseline | completed | target shape, mapping, and guardrails documented |
| Monorepo bootstrap | completed | empty repo initialized with root docs and skeleton |
| Structural import | completed | source modules and deploy assets copied into target tree |
| Verification | completed | exclusion checks passed for git metadata, local state, and local config |
| Runtime isolation cleanup | completed | new monorepo defaults now use separate container names, ports, image names, and compose network |
| Documentation cleanup | completed | imported repo-level docs now use monorepo-native paths and terminology except intentional historical import mapping |
| Root operator scripts | completed | root-level deploy/build entrypoints added for aggregator, MiSub, Cloudflare worker, EasyProxy image, and ech-workers image |
| Follow-up cleanup | pending | root CI, optional config unification, and deeper private-archive decoupling deferred |

## Phase Files

- `docs/progress/phase-01-monorepo-bootstrap.md`
- `docs/progress/phase-02-structural-import.md`
- `docs/progress/phase-03-runtime-isolation.md`
- `docs/progress/phase-04-doc-cleanup.md`
- `docs/progress/phase-05-operator-scripts.md`
