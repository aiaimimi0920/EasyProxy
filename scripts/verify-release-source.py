#!/usr/bin/env python3
"""Reject a release tag that names a different source commit."""

import argparse
import re
import subprocess
import sys


def verify(tag: str, commit: str) -> None:
    # Versions are also directory names in both native installers.
    if not re.fullmatch(r"(?:release-|v)[A-Za-z0-9._-]+", tag) or ".." in tag or tag.endswith((".", ".lock")):
        raise ValueError("release tag must start with release- or v and contain only letters, digits, dot, underscore or hyphen")
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("source commit must be a full Git SHA")
    ref = f"refs/tags/{tag}"
    result = subprocess.run(
        ["git", "ls-remote", "--tags", "origin", ref, f"{ref}^{{}}"],
        check=True, capture_output=True, text=True,
    )
    refs = {name: sha for sha, name in (line.split() for line in result.stdout.splitlines())}
    target = refs.get(f"{ref}^{{}}", refs.get(ref))
    if target is not None and target != commit:
        raise ValueError("release tag points to a different source commit; run the workflow from that tag or choose a new tag")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--commit", required=True)
    args = parser.parse_args()
    try:
        verify(args.tag, args.commit)
    except (ValueError, subprocess.CalledProcessError) as error:
        message = str(error) if isinstance(error, ValueError) else "failed to query release tag; refusing publication"
        print(message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
