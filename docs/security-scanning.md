# Security scan scope

`Security Scan` runs daily at 06:23 UTC, on pull requests, main pushes, and manual dispatch. It has only `contents: read`, uses disposable GitHub-hosted runners, and checks out the exact commit and recursive gitlinks with persisted credentials disabled. It does not use repository secrets, run project builds/install scripts, publish artifacts, deploy, sign, or refresh production. There are no vulnerability baselines, severity exemptions, or unfixed-vulnerability exclusions. Independent matrix jobs continue if another scope fails.

## Inventory

| Scope | Inputs |
| --- | --- |
| Go modules | `service/base/go.mod`, `service/base/third_party/sing-tun/go.mod`, `tools/easyproxyctl/go.mod`, `upstreams/ech-workers/go.mod` |
| npm, including development and transitive dependencies | `service/base/frontend/package-lock.json`, `upstreams/misub/package-lock.json` |
| Python, resolved separately | `requirements-ci.txt`, `upstreams/aggregator/requirements.txt`, `upstreams/aggregator/manager/requirements.txt` |
| Secret patterns | Current source files, including recursive submodules, tests, examples and Markdown |

The source scan traverses the whole checkout, not just this table. Expected dependency targets must appear with nonempty package inventories; otherwise it fails as incomplete. New Python requirement locations must also be added to the matrix and script inventory. Recursive submodule SHAs follow the reviewed parent commit, never remote branch tips.

Python requirements currently lack locked versions. Each job uses Python 3.12 on Linux and pip's isolated, wheel-only dry-run resolver against public PyPI. It does not install dependencies or execute sdist build hooks. Exact resolved direct/transitive versions are recorded and scanned from a generated `requirements.txt`. Missing wheels, incompatible constraints or unavailable registries fail the job; they are not silently skipped. These are the versions resolvable at scan time, **not evidence of deployed versions**, and Windows or another Python version may resolve differently. Only plain package requirements are accepted; URLs, includes, editable/local inputs and pip options fail closed.

## Interpretation and exclusions

- Any advisory or secret finding fails its job, including LOW/UNKNOWN and unfixed advisories. There is no old/new distinction or accepted historical baseline. A future suppression requires explicit independent review.
- Tool/setup/network/database errors and missing expected dependency targets mean **incomplete**, not clean. Trivy collects JSON with exit code 0 so the reporter can print all findings safely; the reporter then exits 1 for findings and 2 for incomplete scans. No workflow `continue-on-error` masks these results.
- Trivy v0.70.0 scans Go module versions and npm locks against advisory databases. Comprehensive mode also estimates the Go standard-library version from go/toolchain directives. This can overreport compared with an actual built binary. It does not determine reachability or inspect application logic.
- The locally replaced `sing-tun` implementation has no independently inferable release version. Its own dependencies are scanned separately; the code changes within that local module are not covered by version-based advisories for the module itself. Fork-specific versions may also lack matching advisories.
- The secret scanner checks the current snapshot, not deleted content or Git history. All 13 global built-in allow rules in the pinned Trivy version are disabled, including tests/examples/Markdown/vendor. Per-rule regex/entropy heuristics and underlying binary/file-format exclusions remain; no tool detects every secret. A synthetic token in a temporary `test.md` verifies actual detection before each secret scan.
- Secret results print only path, rule ID, severity and line numbers. Matches, code excerpts and raw scanner/resolver logs are never uploaded or printed. Raw reports live only in temporary runner directories. Dependency results include advisory IDs, package versions, status and per-target package counts.
- Success means the listed scans completed without detected findings at that database snapshot. It does not establish zero vulnerabilities, safe production state, complete history coverage, or a security audit. No container/OS-image scan, SAST, deployed inventory or live endpoint check is included.

## Tool provenance and maintenance

Workflow actions are pinned to full commit SHAs. Trivy is fixed at v0.70.0; its pinned installer verifies the downloaded release archive against the upstream SHA256 checksum. This is upstream checksum verification, not an independently pinned binary digest. The installer itself pins its nested checkout/cache actions and install-script revision; caching is disabled here. Database content updates at scan time.

Review action/tool upgrades and inventory changes together. Unit tests cover redaction, low/unfixed findings, missing targets, resolver restrictions, transitive pins and tool failure handling. Ordinary repository CI still runs unchanged.

Upstream behavior verified against the pinned sources:

- [Go coverage and local replacement limitations](https://github.com/aquasecurity/trivy/blob/v0.70.0/docs/guide/coverage/language/golang.md)
- [npm development dependencies](https://github.com/aquasecurity/trivy/blob/v0.70.0/docs/guide/coverage/language/nodejs.md)
- [Python exact versions and transitive requirements](https://github.com/aquasecurity/trivy/blob/v0.70.0/docs/guide/coverage/language/python.md)
- [Secret scan configuration](https://github.com/aquasecurity/trivy/blob/v0.70.0/docs/guide/scanner/secret.md) and [global allow rules](https://github.com/aquasecurity/trivy/blob/v0.70.0/pkg/fanal/secret/builtin-allow-rules.go)
