import os
from pathlib import Path
import subprocess
import sys

import pytest


# Builder verification must not trust a clean superproject alone: ignored files
# and skip-worktree flags inside an already prepared gitlink are checked too.
@pytest.mark.parametrize("mode", ["head", "origin", "ignored", "skip-worktree"])
def test_inspects_prepared_gitlink_again_before_build(checkout, mode):
    root, _, rows = checkout
    row = next(item for item in rows if item["path"] == "upstreams/misub")
    sources.inspect_gitlink_checkout(root, row)
    child = root / row["path"]
    if mode == "head":
        (child / "source.txt").write_text("different commit", encoding="utf-8")
        commit(child)
    elif mode == "origin":
        git(child, "remote", "set-url", "origin", "https://github.com/unapproved/misub.git")
    elif mode == "ignored":
        (child / ".git/info/exclude").write_text("private.bin\n", encoding="utf-8")
        (child / "private.bin").write_bytes(b"must-not-enter-build")
    else:
        git(child, "update-index", "--skip-worktree", "source.txt")
    with pytest.raises(sources.SourceError):
        sources.inspect_gitlink_checkout(root, row)


def test_root_accepts_github_case_alias_without_changing_checkout_or_identity(checkout):
    root, sha, expected = checkout
    origin = git(root, "remote", "get-url", "origin")
    assert sources.inspect_checkout(root, "FIXTURE/easyproxy", sha) == expected
    assert git(root, "remote", "get-url", "origin") == origin


@pytest.mark.parametrize("url", ["http://github.com/fixture/EasyProxy.git", "https://github.com/fixture/EasyProxy.git/",
    "https://github.com/fixture/EasyProxy.git?token=denied", "https://github.com/fixture/EasyProxy.git#ref",
    "https://token@github.com/fixture/EasyProxy.git", "https://github.com/other/EasyProxy.git"])
def test_case_alias_does_not_allow_alternate_protocol_credentials_or_repository(url):
    assert not sources._origin_matches(url, "fixture/easyproxy")

# All adapters must share the same exception class even when this suite is
# collected after an adapter has already imported the production module.
sys.path.insert(0, str(Path(__file__).parents[1]))
import sources


def git(root, *args):
    env = os.environ.copy()
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull, GIT_TERMINAL_PROMPT="0")
    return subprocess.run(["git", "-C", str(root), *args], env=env, capture_output=True,
                          encoding="utf-8", errors="strict", check=True, timeout=30).stdout.strip()


def init(path, repository):
    path.mkdir(parents=True)
    git(path, "init", "--quiet")
    git(path, "config", "user.name", "SBA source fixture")
    git(path, "config", "user.email", "fixture@example.invalid")
    git(path, "remote", "add", "origin", f"https://github.com/fixture/{repository}.git")


def commit(path):
    git(path, "add", ".")
    git(path, "commit", "--quiet", "-m", "fixed source fixture")
    sha = git(path, "rev-parse", "HEAD")
    assert sources.SHA.fullmatch(sha) is not None, "fixture Git did not return an exact commit SHA"
    return sha


@pytest.fixture
def checkout(tmp_path):
    root = tmp_path / "application"
    init(root, "EasyProxy")
    modules = []
    expected = []
    for path in sources.PATHS:
        child = root / path
        name = path.split("/")[-1]
        init(child, name)
        (child / "source.txt").write_text(name + " 固定源码\n", encoding="utf-8")
        sha = commit(child)
        git(root, "update-index", "--add", "--cacheinfo", f"160000,{sha},{path}")
        url = f"https://github.com/fixture/{name}.git"
        modules.append(f'[submodule "{path}"]\n\tpath = {path}\n\turl = {url}\n')
        expected.append({"path": path, "repository": url, "sourceSha": sha})
    (root / ".gitmodules").write_text("".join(modules), encoding="utf-8")
    sha = commit(root)
    return root, sha, expected


def test_materializes_only_committed_gitlinks_into_new_directory(checkout, tmp_path):
    root, sha, expected = checkout
    output = tmp_path / "isolated"
    assert sources.materialize_gitlinks(root, output, "fixture/EasyProxy", sha, cache_root=root) == expected
    for row in expected:
        cloned = output / row["path"]
        assert git(cloned, "rev-parse", "HEAD") == row["sourceSha"]
        assert git(cloned, "remote", "get-url", "origin") == row["repository"]
        assert git(cloned, "status", "--porcelain") == ""
    assert git(root, "rev-parse", "HEAD") == sha
    assert git(root, "status", "--porcelain") == ""


def test_unified_workspace_is_clean_buildable_and_never_copies_dirty_cache(checkout, tmp_path):
    root, sha, expected = checkout
    (root / "private.txt").write_text("not approved", encoding="utf-8")
    (root / sources.PATHS[0] / "source.txt").write_text("dirty cache", encoding="utf-8")
    output = tmp_path / "build-workspace"
    assert sources.materialize_workspace(output, "fixture/EasyProxy", sha, cache_root=root) == expected
    assert sources.inspect_checkout(output, "fixture/EasyProxy", sha) == expected
    assert not (output / "private.txt").exists()
    for row in expected:
        assert git(output / row["path"], "rev-parse", "HEAD") == row["sourceSha"]
        assert "固定源码" in (output / row["path"] / "source.txt").read_text(encoding="utf-8")
    assert (root / "private.txt").read_text(encoding="utf-8") == "not approved"
    with pytest.raises(sources.SourceError):
        sources.materialize_workspace(output, "fixture/EasyProxy", sha, cache_root=root)


@pytest.mark.parametrize("mutation", ["untracked", "ignored", "root-source", "submodule-source", "head", "origin", "assume-unchanged"])
def test_rejects_dirty_or_forged_root_before_creating_output(checkout, tmp_path, mutation):
    root, sha, _ = checkout
    if mutation == "untracked":
        (root / "private.txt").write_text("not part of approved source", encoding="utf-8")
    elif mutation == "ignored":
        (root / ".git" / "info" / "exclude").write_text("private.tmp\n", encoding="utf-8")
        (root / "private.tmp").write_text("ignored private source", encoding="utf-8")
    elif mutation == "root-source":
        (root / ".gitmodules").write_text("changed", encoding="utf-8")
    elif mutation == "submodule-source":
        (root / sources.PATHS[0] / "source.txt").write_text("uncommitted", encoding="utf-8")
    elif mutation == "head":
        sha = "a" * 40
    elif mutation == "origin":
        git(root, "remote", "set-url", "origin", "https://github.com/foreign/EasyProxy.git")
    else:
        git(root, "update-index", "--assume-unchanged", ".gitmodules")
    output = tmp_path / "must-not-exist"
    with pytest.raises(sources.SourceError):
        sources.materialize_gitlinks(root, output, "fixture/EasyProxy", sha, cache_root=root)
    assert not output.exists()


@pytest.mark.parametrize("bad", ["https://user:secret@github.com/fixture/aggregator.git", "file:///private/repo", "git@github.com:fixture/aggregator.git", "https://github.com/fixture/aggregator.git?ref=main"])
def test_rejects_unsafe_or_noncanonical_submodule_urls(checkout, bad):
    root, _, _ = checkout
    path = root / ".gitmodules"
    text = path.read_text(encoding="utf-8").replace("https://github.com/fixture/aggregator.git", bad)
    path.write_text(text, encoding="utf-8")
    sha = commit(root)
    with pytest.raises(sources.SourceError, match="SOURCE_REJECTED"):
        sources.read_gitlinks(root, sha)


def test_rejects_existing_destination_and_floating_ref(checkout, tmp_path):
    root, sha, _ = checkout
    output = tmp_path / "existing"
    output.mkdir()
    marker = output / "must-preserve.txt"
    marker.write_text("保留", encoding="utf-8")
    for target, source_sha in [(output, sha), (root / "nested", sha), (tmp_path / "unused", "main")]:
        with pytest.raises(sources.SourceError):
            sources.materialize_gitlinks(root, target, "fixture/EasyProxy", source_sha, cache_root=root)
    assert marker.read_text(encoding="utf-8") == "保留"
    assert not (root / "nested").exists()


def test_reads_git_objects_not_modified_gitmodules(checkout):
    root, sha, expected = checkout
    (root / ".gitmodules").write_text("untrusted working tree", encoding="utf-8")
    assert sources.read_gitlinks(root, sha) == expected


def test_materializes_root_from_approved_object_not_dirty_cache_head(checkout, tmp_path):
    root, sha, expected = checkout
    (root / "unapproved.txt").write_text("not approved", encoding="utf-8")
    newer_sha = commit(root)
    modules = root / ".gitmodules"
    modules.write_text(modules.read_text(encoding="utf-8").replace("fixture/aggregator.git", "private/aggregator.git"), encoding="utf-8")
    before = git(root, "status", "--porcelain")
    output = tmp_path / "approved-root"
    assert sources.materialize_root(output, "fixture/EasyProxy", sha, cache_root=root) == expected
    assert git(output, "rev-parse", "HEAD") == sha
    assert not (output / "unapproved.txt").exists()
    assert sources.inspect_checkout(output, "fixture/EasyProxy", sha) == expected
    assert git(root, "rev-parse", "HEAD") == newer_sha
    assert git(root, "status", "--porcelain") == before


def test_independently_dirty_gitlink_cache_cannot_supply_worktree_code(checkout, tmp_path):
    root, sha, expected = checkout
    cache = tmp_path / "read-only-cache"
    for row in expected:
        target = cache / row["path"]
        init(target, row["path"].split("/")[-1])
        git(target, "fetch", "--quiet", str(root / row["path"]), row["sourceSha"])
        git(target, "checkout", "--quiet", "--detach", row["sourceSha"])
        (target / "source.txt").write_text("uncommitted source must never be deployed", encoding="utf-8")
        (target / "private.txt").write_text("private runtime data", encoding="utf-8")
    output = tmp_path / "fixed-gitlinks"
    assert sources.materialize_gitlinks(root, output, "fixture/EasyProxy", sha, cache_root=cache) == expected
    for row in expected:
        target = output / row["path"]
        assert (target / "source.txt").read_text(encoding="utf-8") == row["path"].split("/")[-1] + " 固定源码\n"
        assert not (target / "private.txt").exists()
        assert (cache / row["path"] / "private.txt").exists()


@pytest.mark.parametrize("source_sha", ["main", "HEAD", "A" * 40, "--upload-pack=untrusted"])
def test_root_preparation_rejects_floating_or_noncanonical_sha_before_creating_output(checkout, tmp_path, source_sha):
    root, _, _ = checkout
    output = tmp_path / "must-not-exist"
    with pytest.raises(sources.SourceError):
        sources.materialize_root(output, "fixture/EasyProxy", source_sha, cache_root=root)
    assert not output.exists()
