import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("security_scan", Path(__file__).resolve().parents[1] / "scripts/security-scan.py")
SCAN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SCAN)


class SecurityScanTests(unittest.TestCase):
    def test_secret_excerpts_never_reach_logs_and_findings_fail(self):
        report = {"SchemaVersion": 2, "Results": [{"Target": "test.md", "Secrets": [{
            "RuleID": "synthetic", "Severity": "LOW", "StartLine": 3,
            "Match": "DO-NOT-LOG", "Code": {"Lines": ["DO-NOT-LOG"]}, "Title": "DO-NOT-LOG",
        }]}]}
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(SCAN.report_findings(report), 1)
        self.assertNotIn("DO-NOT-LOG", output.getvalue())
        self.assertIn('"RuleID": "synthetic"', output.getvalue())

    def test_unfixed_low_severity_is_not_suppressed(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(SCAN.report_findings({"SchemaVersion": 2, "Results": [{
                "Target": "go.mod", "Vulnerabilities": [{"Severity": "LOW", "FixedVersion": ""}]
            }]}), 1)

    def test_missing_or_empty_dependency_results_fail(self):
        with contextlib.redirect_stdout(io.StringIO()):
            for report in ({}, {"SchemaVersion": 2, "Results": []},
                           {"SchemaVersion": 2, "Results": [{"Target": "go.mod", "Packages": []}]}):
                with self.assertRaises(RuntimeError):
                    SCAN.report_findings(report, ("go.mod",))

    def test_packages_without_findings_pass_with_explicit_scope(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(SCAN.report_findings({"SchemaVersion": 2, "Results": [{
                "Target": "go.mod", "Packages": [{"Name": "example"}]
            }]}, ("go.mod",)), 0)

    def test_clean_secret_report_may_omit_results(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(SCAN.report_findings({"SchemaVersion": 2}), 0)

    def test_resolution_rejects_executable_and_external_inputs(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            source = directory / "input.txt"
            for content in ("-r other.txt", "--no-binary=:all:", "thing @ https://invalid/x", "./local"):
                source.write_text(content, encoding="utf-8")
                with patch.object(SCAN, "run_quiet") as run:
                    with self.assertRaises(RuntimeError):
                        SCAN.resolve_python(str(source), directory)
                    run.assert_not_called()

    def test_resolution_keeps_transitive_versions_without_install(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            source = directory / "input.txt"
            source.write_text("requests\npytest>=8.0,<9.0\n", encoding="utf-8")
            def resolve(args):
                self.assertIn("--dry-run", args)
                self.assertIn("--only-binary=:all:", args)
                (directory / "pip-report.json").write_text(json.dumps({"install": [
                    {"metadata": {"name": "requests", "version": "2.32.3"}},
                    {"metadata": {"name": "urllib3", "version": "2.0.0"}},
                ]}), encoding="utf-8")
            with patch.object(SCAN, "run_quiet", side_effect=resolve), contextlib.redirect_stdout(io.StringIO()):
                SCAN.resolve_python(str(source), directory)
            self.assertIn("urllib3==2.0.0", (directory / "requirements.txt").read_text())

    def test_tool_failure_does_not_log_sensitive_diagnostics(self):
        with patch.object(SCAN.subprocess, "run") as run:
            run.return_value.returncode = 42
            run.return_value.stderr = "DO-NOT-LOG"
            with self.assertRaisesRegex(RuntimeError, "exit 42") as caught:
                SCAN.run_quiet(["trivy", "fs"])
            self.assertNotIn("DO-NOT-LOG", str(caught.exception))
