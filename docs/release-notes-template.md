# Release Notes Template

Use this template for an authorized EasyProxy release. Fill in actual values;
delete sections that do not apply. A template does not authorize publishing.

## Identity

- Version/tag: `<tag>`
- Root commit: `<full-commit>`
- Changed upstream submodules: `<path: old-commit -> new-commit>`
- Published artifacts and image digests: `<name / URL / SHA256 or digest>`

## Changes

Summarize operator-visible changes by affected component, including runtime,
WebUI, cloud sources, deployment tooling, and compatibility changes.
Do not list unchanged components or historical migration milestones.

## Deployment And Upgrade

- Supported targets and relevant configuration changes: `<details>`
- Required operator actions: `<steps, or none>`
- Resource names: use the operator's topology or `<deployment>` placeholders,
  not maintainer-specific service names.
- Configuration/data preservation and rollback procedure: `<details>`
- Optional runtime snapshot/import-code changes: `<if applicable>`

Never include credentials, private import bundles, or secret-bearing URLs.
Link to the relevant current operator guide rather than duplicating it.

## Validation

- Actual validation commands/workflow runs: `<links and results>`
- Final artifact startup, API, and proxy tests: `<targets and results>`
- Data preservation and rollback checks: `<results>`
- Not tested or unsupported: `<explicit scope>`

Use [Release Checklist](release-checklist.md) and
[Release Contract](release-contract.md). Do not equate a build or workflow
success with unperformed runtime, deployment, or recovery validation.

## Breaking Changes And Known Limitations

List only changes and limitations that affect operators of this release.
Explain migration or mitigation steps without promising unapproved future work.
