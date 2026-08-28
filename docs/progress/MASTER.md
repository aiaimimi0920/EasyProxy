# EasyProxy Monorepo Migration

## Scope

Historical copy-only migration from the legacy workspace, followed by current
fork-and-submodule ownership for the three upstream-derived modules.

## Status

| Phase | Status | Notes |
| --- | --- | --- |
| Planning baseline | completed | target shape, mapping, and guardrails documented |
| Monorepo bootstrap | completed | empty repo initialized with root docs and skeleton |
| Structural import | completed | source modules and deploy assets copied into target tree |
| Verification | completed | exclusion checks passed for git metadata, local state, and local config |
| Runtime isolation cleanup | completed | new monorepo defaults now use separate container names, ports, image names, and compose network |
| Documentation cleanup | completed | imported repo-level docs now use monorepo-native paths and terminology except intentional historical import mapping |
| Root operator scripts | completed | root-level deploy/build entrypoints added for EasyProxy, aggregator, MiSub, Cloudflare worker, EasyProxy image, and ech-workers image |
| Unified root config | completed | root config example, renderers, and script integration validated end-to-end for generated service config, MiSub .env, and worker .dev.vars |
| Follow-up cleanup | completed | root CI is release-grade and tracked automation no longer probes legacy private-archive paths |
| Fork/submodule ownership | completed | MiSub, Aggregator, and ech-workers are pinned public submodules; recursive fresh-clone validation passed |

## Phase Files

- `docs/progress/phase-01-monorepo-bootstrap.md`
- `docs/progress/phase-02-structural-import.md`
- `docs/progress/phase-03-runtime-isolation.md`
- `docs/progress/phase-04-doc-cleanup.md`
- `docs/progress/phase-05-operator-scripts.md`
- `docs/progress/phase-06-root-config.md`
- `docs/progress/phase-07-follow-up-cleanup.md`
