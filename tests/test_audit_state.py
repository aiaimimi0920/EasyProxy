import importlib.util
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
from unittest.mock import Mock, patch

import pytest

from scripts import easyproxy_audit_state as resources

spec = importlib.util.spec_from_file_location('maintenance_state_tests', Path(__file__).parents[1] / 'scripts/maintain-misub-discovery.py')
maintenance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(maintenance)


def owned():
    state = resources.AuditState('a' * 32)
    state.creation_attempted = True
    return state


def volume_info(state, owner=None):
    return json.dumps([{'Name': state.volume, 'Labels': {resources.OWNER_LABEL: owner or state.owner}}])


@pytest.mark.parametrize('owner', ['', '../foreign', 'a' * 31, 'A' * 32, 'a' * 33])
def test_owner_rejects_non_exact_tokens(owner):
    with pytest.raises(ValueError):
        resources.AuditState(owner)


def test_unique_names_and_no_adoption_of_existing_volume():
    assert resources.AuditState().volume != resources.AuditState().volume
    state = resources.AuditState()
    with patch.object(resources, 'docker', return_value=state.volume) as docker:
        with pytest.raises(RuntimeError, match='already exists'):
            state.create()
        state.cleanup()
    assert docker.call_count == 1


def test_foreign_container_is_never_removed():
    state = owned()
    info = [{'Id': 'container-id', 'Name': '/' + state.container,
             'Config': {'Labels': {resources.OWNER_LABEL: 'foreign'}}}]
    with patch.object(resources, 'docker', side_effect=['container-id', json.dumps(info)]) as docker:
        with pytest.raises(RuntimeError, match='container ownership'):
            state.cleanup()
    assert all('rm' not in call.args for call in docker.call_args_list)


def test_foreign_volume_is_never_removed_or_mounted():
    state = owned()
    with patch.object(resources, 'docker', side_effect=['', state.volume, volume_info(state, 'foreign')]) as docker:
        with pytest.raises(RuntimeError, match='volume ownership'):
            state.cleanup()
    assert all('rm' not in call.args for call in docker.call_args_list)
    with patch.object(resources, 'docker', return_value=volume_info(state, 'foreign')):
        with pytest.raises(RuntimeError, match='volume ownership'):
            state.mount_args()


def test_cleanup_uses_verified_container_id_before_exact_volume():
    state = owned()
    info = [{'Id': 'container-id', 'Name': '/' + state.container,
             'Config': {'Labels': {resources.OWNER_LABEL: state.owner}}}]
    with patch.object(resources, 'docker', side_effect=['container-id', json.dumps(info), '',
                                                       state.volume, volume_info(state), '']) as docker:
        state.cleanup()
    mutations = [call.args for call in docker.call_args_list if 'rm' in call.args]
    assert mutations == [('container', 'rm', '-f', 'container-id'), ('volume', 'rm', state.volume)]
    assert not state.creation_attempted


def test_cleanup_failure_is_not_suppressed():
    state = owned()
    with patch.object(resources, 'docker', side_effect=['', state.volume, volume_info(state), RuntimeError('busy')]):
        with pytest.raises(RuntimeError, match='busy'):
            state.cleanup()
    assert state.creation_attempted


def test_docker_operation_is_bounded_and_diagnostics_are_redacted():
    with patch.object(resources.subprocess, 'run', side_effect=subprocess.TimeoutExpired('sensitive-url', 30)) as run:
        with pytest.raises(RuntimeError) as error:
            resources.docker('volume', 'ls')
    assert 'sensitive' not in str(error.value)
    assert run.call_args.kwargs['timeout'] == 30


@pytest.mark.parametrize('failure', ['timeout', 'bad-summary', 'cleanup'])
def test_audit_exception_never_returns_a_deletion_verdict(tmp_path, failure, capsys):
    state = Mock(owner='a' * 32, audit_id='synthetic')
    if failure == 'cleanup':
        state.cleanup.side_effect = RuntimeError('cleanup failed')

    def audit(command, timeout):
        if failure == 'timeout':
            raise subprocess.TimeoutExpired('sensitive-url', timeout)
        summary = Path(command[command.index('--output-path') + 1])
        summary.write_text('invalid' if failure == 'bad-summary' else json.dumps({'nodes': {'stable_available_count': 1}}))
        return 0

    with patch.object(maintenance, 'AuditState', return_value=state), patch.object(maintenance, 'run_audit', side_effect=audit):
        with pytest.raises(RuntimeError):
            maintenance.audit_nodes('https://sensitive.invalid/token', SimpleNamespace(work_dir=tmp_path, image='synthetic', audit_timeout=1))
    state.cleanup.assert_called_once()
    assert not list(tmp_path.iterdir())
    assert 'sensitive' not in capsys.readouterr().out


def test_later_cleanup_failure_prevents_all_pending_deletions(monkeypatch, capsys):
    monkeypatch.setenv('MISUB_ADMIN_PASSWORD', 'synthetic-secret')
    monkeypatch.setattr(sys, 'argv', ['maintenance', '--apply', '--base-url', 'https://synthetic.invalid'])
    admin, public = Mock(), Mock()
    admin.headers = {}
    admin.post.return_value.json.return_value = {'success': True}
    data = {'misubs': [{'id': name, 'kind': 'subscription', 'enabled': False,
                       'options': {'managed_by': 'aggregator_sync'}} for name in ('first', 'second')], 'profiles': []}
    data_response, settings_response = Mock(), Mock()
    data_response.json.return_value = data
    settings_response.json.return_value = {'aggregatorSync': {'sourceUrl': 'https://synthetic.invalid/discovery'}}
    admin.get.side_effect = [data_response, settings_response]
    public.get.return_value.json.return_value = {}
    with patch.object(maintenance.requests, 'Session', side_effect=[admin, public]), \
            patch.object(maintenance, 'probe_source', side_effect=[('unavailable', []), RuntimeError('cleanup failed')]):
        with pytest.raises(RuntimeError, match='cleanup failed'):
            maintenance.main()
    assert [call.args[0] for call in admin.post.call_args_list] == ['https://synthetic.invalid/api/login']
    output = capsys.readouterr().out
    assert 'patch-apply' not in output and 'synthetic-secret' not in output


def test_audit_subprocess_timeout_terminates_before_return():
    process = Mock()
    process.communicate.side_effect = [subprocess.TimeoutExpired('synthetic', 1), (b'', b'')]
    process.pid = 123456
    context = Mock()
    context.__enter__ = Mock(return_value=process)
    context.__exit__ = Mock(return_value=False)
    with patch.object(maintenance.subprocess, 'Popen', return_value=context), \
            patch.object(maintenance.os, 'killpg', create=True) as killpg:
        with pytest.raises(subprocess.TimeoutExpired):
            maintenance.run_audit(['synthetic'], 1)
    if maintenance.os.name == 'posix':
        killpg.assert_called_once_with(process.pid, maintenance.signal.SIGKILL)
    else:
        process.kill.assert_called_once()
    assert process.communicate.call_count == 2


def test_shared_audit_mounts_owned_volume_without_legacy_container_removal(tmp_path, monkeypatch):
    from scripts import easyproxy_source_audit as audit
    state = resources.AuditState('b' * 32)
    monkeypatch.setattr(sys, 'argv', ['audit', '--audit-id', state.audit_id,
                                    '--maintenance-state-owner', state.owner, '--image', 'synthetic',
                                    '--subscription', 'https://synthetic.invalid', '--artifact-dir', str(tmp_path),
                                    '--docker-network-name', ''])
    with patch.object(audit, 'ensure_docker'), patch.object(audit, 'load_policy', return_value={}), \
            patch.object(audit, 'ensure_image', return_value='synthetic'), \
            patch.object(audit, 'get_free_port', return_value=12345), \
            patch.object(audit, 'get_free_port_range_start', return_value=34000), \
            patch.object(audit, 'build_config', return_value={'synthetic': True}), \
            patch.object(resources, 'docker', return_value=volume_info(state)), \
            patch.object(audit, 'stop_container') as stop, \
            patch.object(audit, 'run', side_effect=RuntimeError('synthetic launch failure')) as run:
        with pytest.raises(RuntimeError, match='synthetic launch'):
            audit.main()
    command = run.call_args.args[0]
    assert 'type=volume,source=' + state.volume + ',target=/var/lib/easyproxy' in command
    assert resources.OWNER_LABEL + '=' + state.owner in command
    assert command[command.index('--name') + 1] == state.container
    assert not (tmp_path / 'data').exists()
    assert not (tmp_path / 'config.yaml').exists()
    stop.assert_not_called()


def test_shared_audit_rejects_keep_state_flags(monkeypatch):
    from scripts import easyproxy_source_audit as audit
    state = resources.AuditState('c' * 32)
    for flag in ('--skip-cleanup', '--keep-artifacts'):
        monkeypatch.setattr(sys, 'argv', ['audit', '--audit-id', state.audit_id,
                                        '--maintenance-state-owner', state.owner, flag])
        with patch.object(audit, 'ensure_docker') as ensure:
            with pytest.raises(ValueError, match='lifecycle'):
                audit.main()
        ensure.assert_not_called()
