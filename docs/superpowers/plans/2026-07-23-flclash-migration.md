# FlClash Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely migrate the local FlClash nodes and compatible routing rules into EasyProxy without duplicating current nodes, exposing credentials, or modifying FlClash files.

**Architecture:** Build a Go migration package on top of EasyProxy's existing Clash YAML parser, expose it through a repository-level PowerShell command, and default every run to a redacted dry-run. Apply uses authenticated EasyProxy APIs, creates missing nodes disabled, probes and Google-tests them before enablement, writes translated rules to a managed local rule file, and records an import manifest for scoped rollback.

**Tech Stack:** Go 1.24, YAML v3, SHA-256 canonical fingerprints, PowerShell, EasyProxy management API, existing node/config managers, pytest/Go tests.

---

### Task 1: Parse and fingerprint FlClash configurations

**Files:**
- Create: `service/base/internal/flclash/model.go`
- Create: `service/base/internal/flclash/parse.go`
- Create: `service/base/internal/flclash/fingerprint.go`
- Create: `service/base/internal/flclash/parse_test.go`
- Add fixture: `service/base/internal/flclash/testdata/redacted-config.yaml`

- [ ] **Step 1: Write failing parser tests**

The redacted fixture contains two SS nodes, one AnyTLS node, select/url-test/fallback groups, TUN/DNS settings, and representative rules. Tests assert counts and ensure the parsed report never contains raw passwords or full URIs.

- [ ] **Step 2: Run and confirm failure**

```powershell
cd service/base
go test ./internal/flclash -run 'TestParse|TestFingerprint' -count=1
```

Expected: package-not-found failure.

- [ ] **Step 3: Implement structured parsing and canonical identity**

Reuse `config.ParseSubscriptionContent` for node conversion, and parse groups/rules/TUN/DNS into migration-only structs. Canonical node identity includes normalized scheme, host, port, user/auth material, TLS, transport, plugin, and query fields. Return only a SHA-256 fingerprint to reports:

```go
type NodeCandidate struct {
    Name string
    Protocol string
    URI string `json:"-"`
    Fingerprint string
}
```

All errors use node name and protocol, never the raw URI.

- [ ] **Step 4: Run tests and commit**

```powershell
cd service/base
gofmt -w internal/flclash
go test ./internal/flclash -run 'TestParse|TestFingerprint' -count=1
cd ../..
git add service/base/internal/flclash
git commit -m "feat(import): parse FlClash configuration"
```

### Task 2: Generate a redacted migration plan and translate rules

**Files:**
- Create: `service/base/internal/flclash/plan.go`
- Create: `service/base/internal/flclash/rules.go`
- Create: `service/base/internal/flclash/plan_test.go`
- Modify: `service/base/internal/routerule/engine_test.go`

- [ ] **Step 1: Write failing plan tests**

Given existing EasyProxy nodes, assert exact-name matches, canonical duplicates, additions, conflicts, unsupported entries, and translated rule order. Include `DOMAIN-SUFFIX`, `DOMAIN-KEYWORD`, `GEOIP`, `DST-PORT`, and `MATCH`; unknown types must be reported, not broadened.

- [ ] **Step 2: Run and confirm failure**

```powershell
cd service/base
go test ./internal/flclash ./internal/routerule -run 'TestPlan|TestTranslate' -count=1
```

Expected: compile failures for `BuildPlan` and `TranslateRules`.

- [ ] **Step 3: Implement redacted planning**

Define report types containing counts, names, protocols, fingerprints, and reasons but no URI/auth fields. Preserve source rule order and convert `MATCH` into the proposed final policy. Emit the generated rule payload separately so apply can write it under `data/imports/<import-id>/rules.yaml`.

- [ ] **Step 4: Run tests and commit**

```powershell
cd service/base
gofmt -w internal/flclash internal/routerule/engine_test.go
go test ./internal/flclash ./internal/routerule -run 'TestPlan|TestTranslate' -count=1
cd ../..
git add service/base/internal/flclash service/base/internal/routerule/engine_test.go
git commit -m "feat(import): plan redacted FlClash migration"
```

### Task 3: Add authenticated preview, apply, status, and rollback APIs

**Files:**
- Modify: `service/base/internal/monitor/server.go`
- Modify: `service/base/internal/monitor/server_test.go`
- Create: `service/base/internal/flclash/apply.go`
- Create: `service/base/internal/flclash/apply_test.go`

- [ ] **Step 1: Write failing API and transaction tests**

Cover authentication, size limits, preview-only behavior, disabled node creation, rule-file atomic write, import-manifest persistence, partial failure rollback, scoped rollback, and response redaction.

- [ ] **Step 2: Run and confirm failure**

```powershell
cd service/base
go test ./internal/flclash ./internal/monitor -run 'TestFlClashImport' -count=1
```

Expected: 404/compile failures for import routes and apply service.

- [ ] **Step 3: Implement endpoints**

Add authenticated routes:

```text
POST /api/import/flclash/preview
POST /api/import/flclash/apply
GET  /api/import/flclash/{id}
POST /api/import/flclash/{id}/rollback
```

Preview accepts YAML content up to 10 MiB and returns only the redacted plan. Apply requires the preview content hash and explicit `confirm: true`, creates additions disabled, writes rules and manifest with temp-file-plus-rename, reloads the candidate runtime, and records created node IDs. Rollback deletes only those IDs and the matching managed rule file.

- [ ] **Step 4: Run tests and commit**

```powershell
cd service/base
gofmt -w internal/flclash internal/monitor/server.go internal/monitor/server_test.go
go test ./internal/flclash ./internal/monitor -run 'TestFlClashImport' -count=1
cd ../..
git add service/base/internal/flclash service/base/internal/monitor
git commit -m "feat(api): apply and roll back FlClash imports"
```

### Task 4: Add the repository-level migration command

**Files:**
- Create: `scripts/import-flclash.ps1`
- Create: `tests/test_import_flclash_script.py`
- Modify: `README.md`

- [ ] **Step 1: Write failing script smoke tests**

Assert the script defaults to dry-run, requires an explicit config path, never prints lines containing password/uuid/server secrets, and requires `-Apply` plus a matching preview hash for mutation.

- [ ] **Step 2: Run and confirm failure**

```powershell
python -m pytest tests/test_import_flclash_script.py -q -s
```

Expected: FAIL because the script is absent.

- [ ] **Step 3: Implement the command**

Supported usage:

```powershell
.\scripts\import-flclash.ps1 \
  -ConfigPath "$env:APPDATA\com.follow\clash\config.yaml" \
  -EasyProxyUrl "http://192.168.15.201:29888"

.\scripts\import-flclash.ps1 \
  -ConfigPath "$env:APPDATA\com.follow\clash\config.yaml" \
  -EasyProxyUrl "http://192.168.15.201:29888" \
  -Apply \
  -ExpectedContentHash '<preview-hash>'
```

Read the management password from a secure prompt or `EASY_PROXY_MANAGEMENT_PASSWORD`; do not accept it as a normal command-line argument. Save only the redacted JSON report when `-ReportPath` is supplied.

- [ ] **Step 4: Run tests and commit**

```powershell
python -m pytest tests/test_import_flclash_script.py -q -s
git add scripts/import-flclash.ps1 tests/test_import_flclash_script.py README.md
git commit -m "feat(cli): migrate FlClash into EasyProxy"
```

### Task 5: Dry-run and apply the real local FlClash migration

**Files:**
- Source-only: `C:\Users\vmjcv\AppData\Roaming\com.follow\clash\config.yaml`
- Runtime-only: `/opt/easyproxy-gateway/data/imports/<import-id>/`

- [ ] **Step 1: Back up EasyProxy and prove FlClash source immutability**

Record the FlClash file SHA-256, export EasyProxy nodes, back up the VM data directory, and record current API node counts. Do not stop FlClash or modify its files.

- [ ] **Step 2: Run dry-run against the live API**

Expected current result:

```text
FlClash nodes: 68
EasyProxy nodes: 70
Exact-name overlap: 56
Additions: 12 AnyTLS
EasyProxy-only: 14
Rules: 1336 translated or explicitly reported
```

Abort apply if the actual difference is materially different or any report contains a credential.

- [ ] **Step 3: Apply missing nodes disabled and validate**

Apply using the preview hash. Probe each created node, then perform Google HTTPS through each candidate. Enable only passing nodes; keep failures disabled with redacted reasons.

- [ ] **Step 4: Verify routing rules and rollback boundary**

Confirm the managed rule file is loaded before remote/default rules, the final policy matches FlClash `MATCH`, and rollback preview lists exactly the resources created by this import. Do not execute rollback after successful acceptance.

- [ ] **Step 5: Final verification and commit**

```powershell
python -m pytest -q -s
cd service/base
go test -count=1 ./...
go vet ./...
cd ../..
git diff --check
```

Recompute the FlClash source SHA-256 and require it to equal the pre-import value. Record only sanitized counts, import ID, and validation results, then commit and push.

