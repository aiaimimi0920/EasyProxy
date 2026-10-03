import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

import pytest

spec = importlib.util.spec_from_file_location('maintenance_diagnostics', Path(__file__).parents[1] / 'scripts/maintain-misub-discovery.py')
maintenance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(maintenance)

SENTINEL = 'https://synthetic.invalid/private?token=DO_NOT_LOG\nAuthorization: Bearer DO_NOT_LOG'


def record(capsys):
    output = capsys.readouterr().out
    assert 'DO_NOT_LOG' not in output and 'synthetic.invalid' not in output
    rows = [json.loads(line) for line in output.splitlines()]
    rows = [row for row in rows if row.get('maintenance_stage') == 'audit-summary-diagnostic']
    assert len(rows) == 1
    row = rows[0]
    assert set(row) == {'maintenance_stage', 'returncode', 'summary_state', 'nodes_object',
                        'pool_probe_object', 'stable_evidence_present', 'error_present', 'verdict'}
    return row


@pytest.mark.parametrize('kind,expected', [
    ('missing', 'missing'), ('unreadable', 'read-error'), ('encoding', 'invalid-encoding'),
    ('json', 'invalid-json'), ('root-shape', 'invalid-shape'), ('nodes-shape', 'invalid-shape'),
])
def test_summary_failure_categories_are_redacted_and_still_fail(tmp_path, capsys, kind, expected):
    summary = tmp_path / 'summary.json'
    if kind == 'encoding':
        summary.write_bytes(b'\xff' + SENTINEL.encode())
    elif kind == 'json':
        summary.write_text(SENTINEL)
    elif kind == 'root-shape':
        summary.write_text(json.dumps([SENTINEL]))
    elif kind == 'nodes-shape':
        summary.write_text(json.dumps({'nodes': SENTINEL, 'error': SENTINEL}))
    if kind == 'unreadable':
        summary = Mock(read_text=Mock(side_effect=PermissionError(SENTINEL)))
    with pytest.raises((OSError, ValueError, RuntimeError)):
        maintenance.read_audit_verdict(summary, 1)
    row = record(capsys)
    assert row['returncode'] == 1 and row['summary_state'] == expected
    assert row['verdict'] == 'not-evaluated'


@pytest.mark.parametrize('result,returncode,verdict', [
    ({'nodes': {'stable_available_uris': [SENTINEL]}}, 0, 'healthy'),
    ({'nodes': {'stable_available_count': 1}, 'error': SENTINEL}, 1, 'unknown'),
    ({}, 1, 'unknown'),
    ({'nodes': {'total_nodes': 1, 'available_nodes': 0},
      'pool_probe': {'attempts': [{'exit_code': 7, 'stderr': 'URLError'}]},
      'error': 'proxy lease output failed across all shared probe targets'}, 2, 'unavailable'),
])
def test_parsed_summary_logs_presence_only_and_preserves_verdict(tmp_path, capsys, result, returncode, verdict):
    summary = tmp_path / 'summary.json'
    summary.write_text(json.dumps(result))
    assert maintenance.read_audit_verdict(summary, returncode) == verdict
    row = record(capsys)
    assert row['returncode'] == returncode and row['summary_state'] == 'parsed'
    assert row['verdict'] == verdict
    assert row['nodes_object'] == isinstance(result.get('nodes'), dict)
    assert row['pool_probe_object'] == isinstance(result.get('pool_probe'), dict)
    assert row['error_present'] == ('error' in result)


def test_non_numeric_status_cannot_inject_diagnostic_text(tmp_path, capsys):
    summary = tmp_path / 'summary.json'
    summary.write_text('{}')
    assert maintenance.read_audit_verdict(summary, SENTINEL) == 'unknown'
    assert record(capsys)['returncode'] is None


@pytest.mark.parametrize('cleanup_failure', [False, True])
def test_rejected_audit_logs_reason_before_cleanup_and_no_completion(tmp_path, capsys, cleanup_failure):
    state = Mock(owner='a' * 32, audit_id='synthetic')
    if cleanup_failure:
        state.cleanup.side_effect = RuntimeError(SENTINEL)

    def audit(command, timeout):
        Path(command[command.index('--output-path') + 1]).write_text(json.dumps({
            'nodes': {'stable_available_count': 1}, 'error': SENTINEL}))
        return 1

    with patch.object(maintenance, 'AuditState', return_value=state), patch.object(maintenance, 'run_audit', side_effect=audit):
        with pytest.raises(RuntimeError):
            maintenance.audit_nodes(SENTINEL, SimpleNamespace(work_dir=tmp_path, image='synthetic', audit_timeout=1))
    output = capsys.readouterr().out
    assert 'DO_NOT_LOG' not in output and 'audit-complete' not in output
    rows = [json.loads(line) for line in output.splitlines()]
    diagnostic = next(row for row in rows if row['maintenance_stage'] == 'audit-summary-diagnostic')
    assert diagnostic['returncode'] == 1 and diagnostic['verdict'] == 'unknown'
    stages = [row['maintenance_stage'] for row in rows]
    assert stages.index('audit-summary-diagnostic') < stages.index('audit-state-cleanup')
    assert ('audit-state-cleanup-complete' in stages) is not cleanup_failure
    assert ('audit-temp-cleanup-complete' in stages) is not cleanup_failure
