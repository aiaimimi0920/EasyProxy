# Risk Assessment

## Primary Risks

### Private Config Leakage

Risk:

- local deployment config or runtime state gets copied into the monorepo

Mitigation:

- exclude `config.yaml`, `data/`, `.env*`, `.dev.vars`, `.wrangler/`,
  `node_modules/`, and `dist/`
- keep `AIRead/` ignored

### Upstream Drift Becomes Hard To Track

Risk:

- once imported into a monorepo, upstream-derived modules become harder to
  distinguish from monorepo-authored code

Mitigation:

- keep upstream-derived code under explicit `upstreams/*` boundaries
- document import workflow in `docs/upstream-sync.md`

### Path Drift In Deployment Docs

Risk:

- copied deployment notes continue pointing at old workspace paths

Mitigation:

- keep monorepo-level docs pointing at the new layout
- accept that some imported deploy notes may still carry historical paths until
  they are revised in later cleanup work

### Build-System Fragmentation

Risk:

- the repository now contains Go, Node, and Worker-oriented modules with
  different native workflows

Mitigation:

- treat Phase 1 as structural migration only
- postpone shared CI and build-tool normalization to follow-up work

### Source Workspace Mutation

Risk:

- migration scripts accidentally modify the original source workspace

Mitigation:

- use copy-only commands
- validate both source and target root paths explicitly
- never run destructive sync commands against the source tree
