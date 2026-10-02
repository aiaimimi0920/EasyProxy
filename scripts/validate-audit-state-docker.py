#!/usr/bin/env python3
"""Synthetic CI-only reproduction; no subscriptions, published ports or credentials."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import uuid

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
    (context / 'service.sh').write_text('#!/bin/sh\nset -eu\nprintf synthetic > /var/lib/easyproxy/runtime/synthetic\n')
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
    finally:
        for container in created:
            remove_owned(container)
        info = json.loads(docker('image', 'inspect', image))[0]
        assert info['Config']['Labels'].get(LABEL) == owner
        docker('image', 'rm', image)
        if (root / 'owner').read_text() == owner:
            shutil.rmtree(root)


if __name__ == '__main__':
    main()
