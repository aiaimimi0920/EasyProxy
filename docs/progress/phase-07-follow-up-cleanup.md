# Phase 07: Follow-Up Cleanup

## Goal

Close the final migration-level gaps so the monorepo can validate and operate
without depending on the legacy workspace or private archive layout.

## Completed

- strengthened `.github/workflows/validate.yml` so root CI now checks:
  - root Python script tests
  - aggregator and MiSub regressions
  - all `service/base` Go tests
  - CRLF-safe, non-writing Go format validation
  - `go vet ./...`
  - frontend tests, lint, and build
  - release-contract validation
  - a clean generated-asset/source diff after the build
- changed `deploy/service/base/scripts/update_ech_preferred_ips.ps1` to use the
  root `config.yaml` as its optional configuration source
- kept command-line arguments authoritative over root config values
- removed automatic reads from `AIRead\密钥\ProxyService\MiSub密钥.json`
- removed the hardcoded production Worker URL fallback
- corrected the script's repository-root calculation
- preserved one-element preferred-IP selections as JSON arrays

## Validation

- focused PowerShell integration tests cover root-config defaults, explicit
  overrides, partial configs, custom-domain selection, and single-IP results
- workflow contract tests require Go format/vet and generated-asset diff gates
- the full root Python suite and release contract validate the final scripts
  and workflow surface

## Remaining Boundary

`AIRead` may still be used as an external operator knowledge base, but it is
not a path contract for tracked automation. Real secrets remain outside Git and
enter the system through ignored root config, environment/platform secrets,
encrypted import codes, or explicit parameters.
