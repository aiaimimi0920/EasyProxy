#!/usr/bin/env python3
"""Read-only snapshot scans; never print secret matches or raw tool output."""

import json
from pathlib import Path
import re
import secrets
import subprocess
import sys
import tempfile


PYTHON_INPUTS = (
    "requirements-ci.txt",
    "upstreams/aggregator/requirements.txt",
    "upstreams/aggregator/manager/requirements.txt",
)
SOURCE_INPUTS = (
    "service/base/go.mod",
    "service/base/third_party/sing-tun/go.mod",
    "tools/easyproxyctl/go.mod",
    "upstreams/ech-workers/go.mod",
    "service/base/frontend/package-lock.json",
    "upstreams/misub/package-lock.json",
)
# All global built-in allow rules from Trivy v0.70.0. Rule-local heuristics remain.
ALLOW_RULES = (
    "dist-info", "tests", "examples", "vendor", "usr-dirs", "locale-dir",
    "markdown", "node.js", "golang", "python", "rubygems", "wordpress",
    "anaconda-log",
)


def emit(record):
    # JSON escaping prevents file contents from becoming workflow commands.
    print(json.dumps(record, ensure_ascii=True), flush=True)


def run_quiet(args):
    result = subprocess.run(args, capture_output=True, text=True, check=False)
    if result.returncode:
        # pip/trivy diagnostics can include URLs or source excerpts with secrets.
        raise RuntimeError(f"{Path(args[0]).name} failed (exit {result.returncode}); scan incomplete")


def resolve_python(source, directory):
    # Accept registry requirements only; reject URLs, includes and pip options.
    # This also prevents future edits from enabling source-build execution.
    lines = Path(source).read_text(encoding="utf-8").splitlines()
    for line in lines:
        requirement = line.strip()
        if requirement and not requirement.startswith("#"):
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*(?:\[[A-Za-z0-9_,.-]+\])?(?:[<>=!~0-9A-Za-z.*,+-]*)", requirement):
                raise RuntimeError(f"unsupported requirement syntax in {source}; scan incomplete")
    report = directory / "pip-report.json"
    run_quiet([
        sys.executable, "-m", "pip", "--isolated", "install", "--dry-run",
        "--ignore-installed", "--only-binary=:all:", "--disable-pip-version-check",
        "--no-input", "--index-url", "https://pypi.org/simple",
        "--report", str(report), "-r", source,
    ])
    packages = json.loads(report.read_text(encoding="utf-8"))["install"]
    if not packages:
        raise RuntimeError("empty Python resolution; scan incomplete")
    pins = []
    for package in packages:
        metadata = package["metadata"]
        name, version = metadata["name"], metadata["version"]
        if not re.fullmatch(r"[A-Za-z0-9_.-]+", name) or not re.fullmatch(r"[A-Za-z0-9.!+_-]+", version):
            raise RuntimeError("invalid resolved package metadata")
        pins.append(f"{name}=={version}")
    (directory / "requirements.txt").write_text("\n".join(sorted(pins)) + "\n", encoding="utf-8")
    emit({"python_source": source, "resolved_packages": sorted(pins), "runtime": sys.version.split()[0]})


def report_findings(report, expected_targets=()):
    if report.get("SchemaVersion") != 2:
        raise RuntimeError("invalid scanner report; scan incomplete")
    # Trivy omits Results for a clean secret-only scan.
    results = report.get("Results", [])
    targets = {item["Target"].replace("\\", "/") for item in results if item.get("Packages")}
    missing = [path for path in expected_targets if not any(t == path or t.endswith("/" + path) for t in targets)]
    count = 0
    for item in results:
        target = item["Target"]
        emit({"target": target, "type": item.get("Type"), "packages": len(item.get("Packages", []))})
        for vuln in item.get("Vulnerabilities", []):
            emit({"target": target, "vulnerability": {key: vuln.get(key) for key in (
                "VulnerabilityID", "PkgName", "InstalledVersion", "FixedVersion", "Severity", "Status"
            )}})
            count += 1
        for secret in item.get("Secrets", []):
            # Never emit Match, Code, Title or source text, even if Trivy redacts it.
            emit({"target": target, "secret": {key: secret.get(key) for key in (
                "RuleID", "Severity", "StartLine", "EndLine"
            )}})
            count += 1
    emit({"findings": count, "missing_dependency_targets": missing,
          "meaning": "snapshot advisory/pattern scan only; not proof of absence of vulnerabilities"})
    if missing:
        raise RuntimeError("dependency targets were not analyzed; scan incomplete")
    return 1 if count else 0


def scan(scope):
    if scope not in ("source", "secrets", *PYTHON_INPUTS):
        raise RuntimeError("unknown scan scope")
    with tempfile.TemporaryDirectory(prefix="easyproxy-security-") as tmp:
        directory = Path(tmp)
        config = directory / "trivy.yaml"
        config.write_text("{}\n", encoding="utf-8")
        ignore = directory / "ignore"
        ignore.write_text("", encoding="utf-8")
        secret_config = directory / "secret.yaml"
        secret_config.write_text("disable-allow-rules:\n" + "".join(f"  - {rule}\n" for rule in ALLOW_RULES), encoding="utf-8")
        target, expected = ".", ()
        scanner = "secret" if scope == "secrets" else "vuln"
        if scope == "source":
            expected = SOURCE_INPUTS
        elif scope in PYTHON_INPUTS:
            target = str(directory / "resolved")
            Path(target).mkdir()
            resolve_python(scope, Path(target))
            expected = ("requirements.txt",)
        output = directory / "scan.json"
        command = [
            "trivy", "fs", "--config", str(config), "--ignorefile", str(ignore),
            "--secret-config", str(secret_config), "--scanners", scanner,
            "--include-dev-deps", "--detection-priority", "comprehensive",
            "--ignore-unfixed=false", "--severity", "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL",
            "--list-all-pkgs", "--format", "json", "--output", str(output),
            "--exit-code", "0", "--cache-dir", str(directory / "cache"),
            "--timeout", "10m",
        ]
        if scope == "secrets":
            # Prove the actual binary detects a synthetic token in an otherwise
            # globally allowed test/Markdown path. Never emit the token/report.
            canary = directory / "canary"
            canary.mkdir()
            token = "ghp_" + secrets.token_hex(18)
            (canary / "test.md").write_text(token, encoding="utf-8")
            run_quiet(command + [str(canary)])
            probe = json.loads(output.read_text(encoding="utf-8"))
            if not any(item.get("Secrets") for item in probe.get("Results", [])):
                raise RuntimeError("secret scanner positive control failed; scan incomplete")
            emit({"secret_positive_control": "passed", "scope": "current recursive source snapshot only"})
        run_quiet(command + [target])
        return report_findings(json.loads(output.read_text(encoding="utf-8")), expected)


if __name__ == "__main__":
    try:
        sys.exit(scan(sys.argv[1]))
    except (RuntimeError, OSError, ValueError, KeyError, TypeError, IndexError) as error:
        emit({"scan_error": str(error), "status": "incomplete"})
        sys.exit(2)
