import importlib.util
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
from unittest.mock import Mock, patch
from contextlib import ExitStack

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
    if failure == 'cleanup':
        assert list(tmp_path.iterdir())  # Failed resource cleanup must preserve its files.
    else:
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
    with patch.object(maintenance.sys, 'platform', 'linux'), \
            patch.object(maintenance.requests, 'Session', side_effect=[admin, public]), \
            patch.object(maintenance, 'probe_source', side_effect=[('unavailable', []), RuntimeError('cleanup failed')]):
        with pytest.raises(RuntimeError, match='cleanup failed'):
            maintenance.main()
    assert [call.args[0] for call in admin.post.call_args_list] == ['https://synthetic.invalid/api/login']
    output = capsys.readouterr().out
    assert 'patch-apply' not in output and 'synthetic-secret' not in output


@pytest.mark.parametrize('kill_wait_timeout', [False, True])
def test_audit_subprocess_timeout_terminates_before_return(kill_wait_timeout):
    process = Mock()
    process.communicate.side_effect = [subprocess.TimeoutExpired('synthetic', 1),
                                      subprocess.TimeoutExpired('synthetic', 30) if kill_wait_timeout else (b'', b'')]
    process.pid = 123456
    with patch.object(maintenance.sys, 'platform', 'linux'), \
            patch.object(maintenance.signal, 'SIGKILL', 9, create=True), \
            patch.object(maintenance.subprocess, 'Popen', return_value=process), \
            patch.object(maintenance.os, 'killpg', create=True) as killpg:
        with pytest.raises(maintenance.AuditTerminationUnconfirmed if kill_wait_timeout else subprocess.TimeoutExpired):
            maintenance.run_audit(['synthetic'], 1)
    killpg.assert_called_once_with(process.pid, 9)
    assert process.communicate.call_count == 2
    process.wait.assert_not_called()
    process.stdout.close.assert_called_once()
    process.stderr.close.assert_called_once()


def test_non_linux_maintenance_fails_before_requests_or_process_launch(monkeypatch):
    monkeypatch.setattr(sys, 'argv', ['maintenance', '--apply'])
    with patch.object(maintenance.sys, 'platform', 'win32'), \
            patch.object(maintenance.requests, 'Session') as session, \
            patch.object(maintenance.subprocess, 'Popen') as popen:
        with pytest.raises(RuntimeError, match='requires Linux'):
            maintenance.main()
        with pytest.raises(RuntimeError, match='requires Linux'):
            maintenance.run_audit(['synthetic'], 1)
    session.assert_not_called()
    popen.assert_not_called()


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


@pytest.mark.parametrize('summary', [
    {'nodes': {'stable_available_count': 1}, 'error': 'container did not join expected docker network'},
    {'nodes': {'stable_available_uris': ['ss://synthetic']}},
    {'nodes': {'total_nodes': 1, 'available_nodes': 0},
     'pool_probe': {'attempts': [{'exit_code': 7, 'stderr': 'URLError'}]},
     'error': 'proxy lease output failed across all shared probe targets'},
])
def test_nonzero_exit_blocks_all_pending_deletions_even_with_completed_summary(tmp_path, monkeypatch, summary):
    monkeypatch.setenv('MISUB_ADMIN_PASSWORD', 'synthetic-secret')
    monkeypatch.setattr(sys, 'argv', ['maintenance', '--apply', '--base-url', 'https://synthetic.invalid',
                                    '--work-dir', str(tmp_path)])
    admin, public = Mock(), Mock()
    admin.headers = {}
    admin.post.return_value.json.return_value = {'success': True}
    data = {'misubs': [{'id': name, 'kind': 'subscription', 'enabled': False,
                       'options': {'managed_by': 'aggregator_sync'}} for name in ('first', 'second')], 'profiles': []}
    data_response, settings_response = Mock(), Mock()
    data_response.json.return_value = data
    settings_response.json.return_value = {'aggregatorSync': {'sourceUrl': 'https://synthetic.invalid/discovery'}}
    admin.get.side_effect = lambda url, **kwargs: settings_response if url.endswith('/settings') else data_response
    public.get.return_value.json.return_value = {}
    state = Mock(owner='d' * 32, audit_id='synthetic')

    def audit(command, timeout):
        Path(command[command.index('--output-path') + 1]).write_text(json.dumps(summary))
        return 1  # Infrastructure error, including an exception after writing the summary.

    def probe(source, session, args):
        if source['id'] == 'first':
            return 'unavailable', ['unavailable', 'unavailable']
        return maintenance.audit_nodes('https://synthetic.invalid', args), []

    with patch.object(maintenance.sys, 'platform', 'linux'), \
            patch.object(maintenance.requests, 'Session', side_effect=[admin, public]), \
            patch.object(maintenance, 'AuditState', return_value=state), \
            patch.object(maintenance, 'run_audit', side_effect=audit), \
            patch.object(maintenance, 'probe_source', side_effect=probe):
        with pytest.raises(RuntimeError, match='did not complete'):
            maintenance.main()
    state.cleanup.assert_called_once()
    assert [call.args[0] for call in admin.post.call_args_list] == ['https://synthetic.invalid/api/login']
    assert not list(tmp_path.iterdir())


@pytest.mark.parametrize('owned_mode,cleanup_error', [(True, False), (True, True), (False, False)])
def test_expected_negative_exit_requires_successful_finally(tmp_path, monkeypatch, owned_mode, cleanup_error):
    from scripts import easyproxy_source_audit as audit
    state = resources.AuditState('e' * 32)
    argv = ['audit', '--audit-id', state.audit_id, '--image', 'synthetic', '--subscription', 'https://synthetic.invalid',
            '--artifact-dir', str(tmp_path), '--docker-network-name', '', '--scenario-timeout-seconds', '1']
    if owned_mode:
        argv.extend(['--maintenance-state-owner', state.owner])
    monkeypatch.setattr(sys, 'argv', argv)
    with ExitStack() as stack:
        for name, value in {'ensure_docker': None, 'load_policy': {}, 'ensure_image': 'synthetic',
                            'get_free_port': 12345, 'get_free_port_range_start': 34000,
                            'build_config': {'synthetic': True}, 'wait_management_ready': {},
                            'wait_scenario_state': {}, 'collect_container_networks': [],
                            'fetch_nodes_and_source_sync': ({'total_nodes': 1, 'available_nodes': 0}, {}),
                            'collect_direct_probe_candidates': [], 'discover_directly_usable_nodes': ([], []),
                            'probe_http_proxy': {'ok': False, 'attempts': [{'exit_code': 7, 'stderr': 'URLError'}]},
                            'stop_container': None}.items():
            stack.enter_context(patch.object(audit, name, return_value=value))
        stack.enter_context(patch.object(audit.requests, 'get', return_value=Mock(json=lambda: {})))
        stack.enter_context(patch.object(audit.time, 'time', side_effect=[0, 0, 2]))
        stack.enter_context(patch.object(audit.time, 'sleep'))
        stack.enter_context(patch.object(audit, 'run', return_value=SimpleNamespace(returncode=0, stdout='', stderr='')))
        stack.enter_context(patch.object(resources, 'docker', return_value=volume_info(state)))
        if cleanup_error:
            stack.enter_context(patch.object(Path, 'unlink', side_effect=PermissionError('synthetic cleanup failure')))
            with pytest.raises(PermissionError):
                audit.main()
        elif owned_mode:
            assert audit.main() == 2
        else:
            with pytest.raises(audit.AuditProbeUnavailable):
                audit.main()  # Existing non-maintenance CLI still maps exceptions to exit 1.
    summary = json.loads((tmp_path / 'summary.json').read_text())
    assert summary['error'] == 'proxy lease output failed across all shared probe targets'
    assert maintenance.classify_audit(summary, 1 if cleanup_error else 2) == ('unknown' if cleanup_error else 'unavailable')


@pytest.mark.parametrize('failure_mode', ['wait-timeout', 'kill-error', 'pipe-error'])
def test_unconfirmed_termination_retains_resources_and_blocks_all_deletions(tmp_path, monkeypatch, capsys, failure_mode):
    import gc
    monkeypatch.setenv('MISUB_ADMIN_PASSWORD', 'synthetic-secret')
    monkeypatch.setattr(sys, 'argv', ['maintenance', '--apply', '--base-url', 'https://synthetic.invalid',
                                    '--work-dir', str(tmp_path)])
    admin, public = Mock(), Mock()
    admin.headers = {}
    admin.post.return_value.json.return_value = {'success': True}
    data = {'misubs': [{'id': name, 'kind': 'subscription', 'enabled': False,
                       'options': {'managed_by': 'aggregator_sync'}} for name in ('first', 'second')], 'profiles': []}
    data_response, settings_response = Mock(), Mock()
    data_response.json.return_value = data
    settings_response.json.return_value = {'aggregatorSync': {'sourceUrl': 'https://synthetic.invalid/discovery'}}
    admin.get.side_effect = lambda url, **kwargs: settings_response if url.endswith('/settings') else data_response
    public.get.return_value.json.return_value = {}
    state = Mock(owner='f' * 32, audit_id='misub-retirement-' + 'f' * 32)
    process = Mock(pid=123456)
    process.communicate.side_effect = [subprocess.TimeoutExpired('synthetic', 1), subprocess.TimeoutExpired('synthetic', 30)]
    if failure_mode == 'pipe-error':
        process.stdout.close.side_effect = OSError('synthetic close error')
    retained = []

    def start_process(command, **kwargs):
        directory = Path(command[command.index('--artifact-dir') + 1])
        (directory / 'config.yaml').write_text('synthetic state still in use')
        retained.append(directory)
        return process

    def probe(source, session, args):
        if source['id'] == 'first':
            return 'unavailable', ['unavailable', 'unavailable']
        return maintenance.audit_nodes('https://synthetic.invalid', args), []

    with patch.object(maintenance.sys, 'platform', 'linux'), \
            patch.object(maintenance.signal, 'SIGKILL', 9, create=True), \
            patch.object(maintenance.requests, 'Session', side_effect=[admin, public]), \
            patch.object(maintenance, 'AuditState', return_value=state), \
            patch.object(maintenance, 'probe_source', side_effect=probe), \
            patch.object(maintenance.subprocess, 'Popen', side_effect=start_process), \
            patch.object(maintenance.os, 'killpg', create=True, side_effect=OSError('synthetic kill error') if failure_mode == 'kill-error' else None), \
            patch.object(maintenance.shutil, 'rmtree') as rmtree:
        with pytest.raises(maintenance.AuditTerminationUnconfirmed):
            maintenance.main()
        gc.collect()  # No TemporaryDirectory finalizer may delete the retained tree.
        rmtree.assert_not_called()
    state.cleanup.assert_not_called()
    assert len(retained) == 1 and (retained[0] / 'config.yaml').read_text() == 'synthetic state still in use'
    assert [call.args[0] for call in admin.post.call_args_list] == ['https://synthetic.invalid/api/login']
    process.wait.assert_not_called()
    output = capsys.readouterr().out
    assert 'audit-termination-unconfirmed-resources-retained' in output
    assert state.owner in output
    assert all(marker not in output for marker in ('audit-complete', 'audit-state-cleanup', 'audit-temp-cleanup', 'patch-apply'))
    assert 'synthetic-secret' not in output
