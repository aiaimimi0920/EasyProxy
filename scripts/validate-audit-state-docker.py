#!/usr/bin/env python3
"""Synthetic CI-only reproduction; no subscriptions, published ports or credentials."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
from types import SimpleNamespace
from unittest.mock import patch

from easyproxy_audit_state import AuditState

LABEL = 'org.easyproxy.audit-state-test.owner'


def docker(*args):
    result = subprocess.run(['docker', *args], capture_output=True, text=True, timeout=120)
    if result.returncode:
        raise RuntimeError('synthetic Docker operation failed')
    return result.stdout.strip()


def main():
    assert sys.version_info[:3] == (3, 12, 14)
    assert os.getuid() != 0, 'test requires an unprivileged runner'
    owner = uuid.uuid4().hex
    image = 'easyproxy-audit-state-test:' + owner
    name = 'easyproxy-audit-state-test-' + owner
    root = Path(tempfile.mkdtemp(prefix='easyproxy-audit-state-test-')).resolve()
    (root / 'owner').write_text(owner)
    context = root / 'image'
    context.mkdir()
    shutil.copyfile(Path(__file__).resolve().parents[1] / 'deploy/service/base/docker-entrypoint.sh', context / 'entrypoint.sh')
    (context / 'service.sh').write_text('#!/bin/sh\nset -eu\nprintf synthetic > /var/lib/easyproxy/runtime/synthetic\nsleep "${TEST_SLEEP:-0}"\n')
    (context / 'Dockerfile').write_text('''FROM python:3.12.14-slim-bookworm
RUN useradd --uid 10001 --create-home easy && mkdir -p /etc/easyproxy/bootstrap
COPY entrypoint.sh /entrypoint.sh
COPY service.sh /usr/local/bin/easy-proxy
RUN chmod 755 /entrypoint.sh /usr/local/bin/easy-proxy
ENTRYPOINT ["/entrypoint.sh"]
''')
    created = []

    def remove_owned(container):
        info = json.loads(docker('container', 'inspect', container))[0]
        assert info['Config']['Labels'].get(LABEL) == owner
        docker('container', 'rm', '-f', info['Id'])

    try:
        docker('build', '--label', LABEL + '=' + owner, '-t', image, str(context))
        temporary = tempfile.TemporaryDirectory(prefix='audit-', dir=root, ignore_cleanup_errors=True)
        directory = Path(temporary.name)
        data = directory / 'data'
        data.mkdir()
        config = directory / 'config.yaml'
        config.write_text('synthetic: true\n')
        runner_uid = data.stat().st_uid
        container = docker('create', '--name', name, '--label', LABEL + '=' + owner,
                           '--network', 'none', '-e', 'EASY_PROXY_RUN_AS_ROOT=1',
                           '-v', str(data) + ':/var/lib/easyproxy',
                           '-v', str(config) + ':/var/lib/easyproxy/config/config.yaml', image)
        created.append(container)
        docker('start', container)
        assert docker('wait', container) == '0'
        assert data.stat().st_uid == 10001 and runner_uid != 10001
        print('PASS entrypoint transferred synthetic bind-state ownership to UID 10001', flush=True)
        try:
            temporary.cleanup()
        except PermissionError:
            print('PASS Python 3.12.14 cleanup raised PermissionError with ignore_cleanup_errors=True', flush=True)
        else:
            raise AssertionError('expected cleanup PermissionError')
        remove_owned(container)
        created.remove(container)
        # Delete only this test's synthetic tree; never chmod/chown the host back.
        assert directory.parent == root and (root / 'owner').read_text() == owner
        cleaner = docker('create', '--name', name + '-cleanup', '--label', LABEL + '=' + owner,
                         '--network', 'none', '--entrypoint', 'python',
                         '-v', str(directory) + ':/owned', image, '-c',
                         'import pathlib,shutil; p=pathlib.Path("/owned"); '
                         'shutil.rmtree(p/"data"); (p/"config.yaml").unlink(missing_ok=True)')
        created.append(cleaner)
        docker('start', cleaner)
        assert docker('wait', cleaner) == '0'
        remove_owned(cleaner)
        created.remove(cleaner)
        temporary.cleanup()
        print('PASS bounded synthetic cleanup completed without host ACL changes', flush=True)
        validate_owned_state(image, root)
    finally:
        for container in created:
            remove_owned(container)
        info = json.loads(docker('image', 'inspect', image))[0]
        assert info['Config']['Labels'].get(LABEL) == owner
        docker('image', 'rm', image)
        if (root / 'owner').read_text() == owner:
            shutil.rmtree(root)


def validate_owned_state(image, root):
    import importlib.util
    spec = importlib.util.spec_from_file_location('maintenance', Path(__file__).with_name('maintain-misub-discovery.py'))
    maintenance = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(maintenance)
    validate_real_process_timeout(maintenance, root)
    args = SimpleNamespace(work_dir=str(root), image=image, audit_timeout=1)
    for fail in (False, True):
        observed = []

        def synthetic_audit(command, timeout):
            state = AuditState(command[command.index('--maintenance-state-owner') + 1])
            observed.append(state)
            directory = Path(command[command.index('--artifact-dir') + 1])
            config = directory / 'config.yaml'
            config.write_text('synthetic: true\n')
            container = docker('create', '--name', state.container, '--network', 'none',
                               '-e', 'EASY_PROXY_RUN_AS_ROOT=1', '-e', 'TEST_SLEEP=' + ('60' if fail else '0'),
                               *state.mount_args(), '-v', str(config) + ':/var/lib/easyproxy/config/config.yaml', image)
            docker('start', container)
            if fail:
                raise subprocess.TimeoutExpired('synthetic-audit', timeout)
            assert docker('wait', container) == '0'
            Path(command[command.index('--output-path') + 1]).write_text(json.dumps({'nodes': {'stable_available_count': 1}}))
            return 0

        with patch.object(maintenance, 'run_audit', side_effect=synthetic_audit):
            try:
                verdict = maintenance.audit_nodes('https://synthetic.invalid/never-requested', args)
            except RuntimeError:
                assert fail
            else:
                assert not fail and verdict == 'healthy'
        assert len(observed) == 1
        state = observed[0]
        assert not state.volume_exists()
        assert docker('container', 'ls', '-aq', '--filter', 'name=^/' + state.container + '$') == ''
        assert not list(root.glob('misub-retirement-*'))
        print('PASS owned-volume ' + ('timeout fails closed' if fail else 'success preserves verdict') + '; container, volume and temp files removed', flush=True)


def validate_real_process_timeout(maintenance, root):
    record = root / 'synthetic-processes.json'
    descendant = 'import time; marker=' + repr(str(root)) + '; time.sleep(60)'
    command = ('import subprocess,sys,os,json,time,pathlib; '
               'child=subprocess.Popen([sys.executable,"-c",' + repr(descendant) + ']); '
               'pathlib.Path(' + repr(str(record)) + ').write_text(json.dumps('
               '{"parent":os.getpid(),"child":child.pid,"group":os.getpgrp()})); time.sleep(60)')
    started = time.monotonic()
    try:
        try:
            maintenance.run_audit([sys.executable, '-c', command], 2)
        except subprocess.TimeoutExpired:
            pass
        else:
            raise AssertionError('real synthetic process did not time out')
        assert time.monotonic() - started < 10
        pids = json.loads(record.read_text())
        assert pids['group'] == pids['parent']
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            if all(not Path('/proc', str(pids[key])).exists() for key in ('parent', 'child')):
                break
            time.sleep(0.05)
        assert all(not Path('/proc', str(pids[key])).exists() for key in ('parent', 'child'))
        print('PASS real Linux audit timeout terminated parent and descendant; both PIDs disappeared', flush=True)
    finally:
        if record.exists():
            pids = json.loads(record.read_text())
            for key in ('parent', 'child'):
                pid = pids[key]
                try:
                    cmdline = Path('/proc', str(pid), 'cmdline').read_bytes()
                    if str(root).encode() in cmdline and os.getpgid(pid) == pids['group']:
                        os.killpg(pids['group'], 9)
                        break
                except (FileNotFoundError, ProcessLookupError):
                    pass


if __name__ == '__main__':
    main()
