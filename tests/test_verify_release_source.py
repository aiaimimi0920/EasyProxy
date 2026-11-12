import importlib.util
import subprocess
from pathlib import Path

import pytest
import yaml


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("verify_release_source", ROOT / "scripts/verify-release-source.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


@pytest.fixture
def repository(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    def git(*args):
        return subprocess.check_output(["git", *args], text=True).strip()
    git("init", "--quiet")
    git("config", "user.name", "Release test")
    git("config", "user.email", "release@example.invalid")
    git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "source")
    git("remote", "add", "origin", str(tmp_path))
    return git, git("rev-parse", "HEAD")


@pytest.mark.parametrize("annotated", [False, True])
def test_existing_tag_must_match_source(repository, annotated):
    git, sha = repository
    if annotated:
        git("-c", "tag.gpgsign=false", "tag", "-a", "v-test", "-m", "release")
    else:
        git("-c", "tag.gpgsign=false", "tag", "v-test")
    module.verify("v-test", sha)
    git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "different source")
    with pytest.raises(ValueError, match="different source commit"):
        module.verify("v-test", git("rev-parse", "HEAD"))


def test_new_tag_is_allowed_without_creating_it(repository):
    git, sha = repository
    module.verify("release-new", sha)
    assert git("tag", "--list") == ""


@pytest.mark.parametrize("tag", ["v$(id)", "v'; echo unsafe; '", "v1\nextra=value", "v1/../../data", "main"])
def test_unsafe_version_is_rejected_before_git(tag, monkeypatch):
    def unexpected(*args, **kwargs):
        pytest.fail("invalid version must not query Git")
    monkeypatch.setattr(module.subprocess, "run", unexpected)
    with pytest.raises(ValueError, match="release tag"):
        module.verify(tag, "a" * 40)


def test_remote_failure_is_not_treated_as_absent_tag(repository):
    git, sha = repository
    git("remote", "set-url", "origin", "missing-repository")
    with pytest.raises(subprocess.CalledProcessError):
        module.verify("v-test", sha)


def test_workflow_checks_source_before_build_and_before_publication():
    workflow = yaml.safe_load((ROOT / ".github/workflows/publish-github-release.yml").read_text(encoding="utf-8"))
    jobs = workflow["jobs"]
    for name in ("metadata", "publish-release"):
        commands = "\n".join(step.get("run", "") for step in jobs[name]["steps"])
        assert "python scripts/verify-release-source.py" in commands
    metadata = next(step for step in jobs["metadata"]["steps"] if step.get("id") == "release_meta")
    assert "${{ inputs.release_tag }}" not in metadata["run"]
    assert metadata["env"]["INPUT_RELEASE_TAG"] == "${{ inputs.release_tag }}"
    for name in ("metadata", "build-native", "publish-release"):
        checkout = next(step for step in jobs[name]["steps"] if step.get("uses", "").startswith("actions/checkout@"))
        assert checkout["with"]["ref"] == "${{ github.sha }}"
