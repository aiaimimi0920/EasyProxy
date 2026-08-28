# Upstream Sync

## Purpose

The root repository pins three public maintained forks as submodules:

| Root path | Maintained fork | Official upstream |
| --- | --- | --- |
| `upstreams/misub` | `aiaimimi0920/MiSub` | `imzyb/MiSub` |
| `upstreams/aggregator` | `aiaimimi0920/aggregator` | `wzdnzd/aggregator` |
| `upstreams/ech-workers` | `aiaimimi0920/ech-workers` | `hhsw2015/ech-workers` |

Each fork contains `UPSTREAM.md` with its audited baseline, EasyProxy delta,
and module validation contract.

## Rule

Upstream-derived source is never copied into the root repository. Source
changes merge into the maintained fork first. The root pull request changes the
submodule pointer only after the fork commit and cross-module behavior verify.

## Maintainer Workflow

1. Clone the maintained fork and add the official repository as `upstream`.
2. Fetch upstream and create `sync/<date>-<upstream-short-sha>` from fork main.
3. Merge or cherry-pick the intended upstream commits; never force-push main.
4. Resolve conflicts without dropping the EasyProxy delta or regression tests.
5. Update `UPSTREAM.md` when the audited baseline or divergence changes.
6. Run the module-native test/build contract and open a fork pull request.
7. After that pull request merges, update exactly one root submodule pointer.
8. Use the upstream-sync root pull request template and run root validation.
9. Merge the root pointer update only after recursive fresh-clone validation.

Typical root pointer update:

```powershell
git submodule sync --recursive
git -C upstreams/misub fetch origin main
git -C upstreams/misub checkout <verified-fork-commit>
git add upstreams/misub
```

Replace the path for Aggregator or ech-workers. Do not use
`git submodule update --remote` in release automation because it removes the
reviewed commit pin.

## Pull Request Evidence

The root pull request must record:

- official upstream URL and old/new upstream commits
- maintained fork pull request and resulting full commit
- intentional EasyProxy deltas retained or changed
- module test/build results
- root regression and recursive clone results
- rollback pointer (the previous root gitlink commit)
- data/deployment impact, including an explicit `none` when applicable

## Guardrails

- Do not edit a detached submodule and leave an unpushed commit referenced by
  the root repository.
- Do not point `.gitmodules` at private or contributor-specific forks.
- Do not combine upstream sync with data migration or secret rotation.
- Do not scatter `aggregator` or `ech-workers` code into `service/base`.
- Keep deployment config sanitized and public-safe inside `deploy/`.
- Keep private credentials, `.env`, databases, and generated artifacts out of
  maintained forks.

## Self-Owned Areas

`workers/ech-workers-cloudflare` is self-owned in this repository. It does not
follow the fork/submodule workflow above, but deployment notes and
private credentials must still stay separated.
