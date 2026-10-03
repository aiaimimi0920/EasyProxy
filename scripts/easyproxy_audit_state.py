"""Owned, ephemeral Docker state for discovery maintenance only."""
import json
import re
import subprocess
import uuid

OWNER_LABEL = 'org.easyproxy.discovery-audit.owner'


def docker(*args):
    try:
        result = subprocess.run(['docker', *args], capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        raise RuntimeError('audit resource operation failed') from None
    if result.returncode:
        raise RuntimeError('audit resource operation failed')
    return result.stdout.strip()


class AuditState:
    def __init__(self, owner=None):
        self.owner = uuid.uuid4().hex if owner is None else owner
        if not re.fullmatch('[0-9a-f]{32}', self.owner):
            raise ValueError('invalid audit resource owner')
        self.volume = 'easyproxy-discovery-state-' + self.owner
        self.audit_id = 'misub-retirement-' + self.owner
        self.container = 'easyproxy-source-audit-' + self.audit_id
        self.creation_attempted = False

    def volume_exists(self):
        return self.volume in docker('volume', 'ls', '--format', '{{.Name}}').splitlines()

    def verify_volume(self):
        info = json.loads(docker('volume', 'inspect', self.volume))[0]
        if info.get('Name') != self.volume or (info.get('Labels') or {}).get(OWNER_LABEL) != self.owner:
            raise RuntimeError('audit volume ownership mismatch')

    def create(self):
        if self.volume_exists():
            raise RuntimeError('audit volume already exists')
        self.creation_attempted = True
        docker('volume', 'create', '--label', OWNER_LABEL + '=' + self.owner, self.volume)
        self.verify_volume()

    def mount_args(self):
        self.verify_volume()
        return ['--label', OWNER_LABEL + '=' + self.owner,
                '--mount', 'type=volume,source=' + self.volume + ',target=/var/lib/easyproxy']

    def cleanup(self):
        if not self.creation_attempted:
            return
        ids = docker('container', 'ls', '-aq', '--no-trunc', '--filter',
                     'name=^/' + self.container + '$').splitlines()
        if len(ids) > 1:
            raise RuntimeError('ambiguous audit container')
        for container_id in ids:
            info = json.loads(docker('container', 'inspect', container_id))[0]
            labels = info.get('Config', {}).get('Labels') or {}
            if (info.get('Id') != container_id or info.get('Name') != '/' + self.container
                    or labels.get(OWNER_LABEL) != self.owner):
                raise RuntimeError('audit container ownership mismatch')
            docker('container', 'rm', '-f', container_id)
        if self.volume_exists():
            self.verify_volume()
            # Docker refuses removal if another container still uses this volume.
            docker('volume', 'rm', self.volume)
        self.creation_attempted = False
