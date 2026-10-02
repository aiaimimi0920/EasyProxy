#!/usr/bin/env python3
"""审核已停用或上游缺失的自动发现来源；不把抓取异常当成删除依据。"""
from __future__ import annotations

import argparse
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import uuid

import requests


def is_managed(source):
    return source.get('kind') == 'subscription' and source.get('options', {}).get('managed_by') == 'aggregator_sync'


def classify_response(response):
    # 拦截、限流、服务器故障、TLS/DNS 异常不能证明订阅已退役。
    if response.status_code in (404, 410):
        return 'unavailable'
    if response.status_code != 200:
        return 'unknown'
    text = response.text[:200000].lstrip().lower()
    if any(word in text for word in ('cf-chl-', 'captcha', 'challenge-platform', 'just a moment', 'access denied')):
        return 'unknown'
    if text.startswith(('<!doctype html', '<html')):
        return 'unavailable'
    return 'audit'


def classify_audit(result, returncode):
    nodes = result.get('nodes') or {}
    if nodes.get('stable_available_uris') or nodes.get('stable_available_count', 0) > 0:
        return 'healthy'
    # 只接受已加载节点、完成网络探测后的明确失败，不接受容器启动/构建失败。
    if (returncode != 0 and nodes.get('total_nodes', 0) > 0
            and result.get('error') == 'proxy lease output failed across all shared probe targets'
            and nodes.get('available_nodes', 0) == 0):
        return 'unavailable'
    return 'unknown'


def audit_nodes(url, args):
    with tempfile.TemporaryDirectory(prefix='misub-retirement-', dir=args.work_dir, ignore_cleanup_errors=True) as tmp:
        summary = Path(tmp) / 'summary.json'
        audit_id = 'misub-retirement-' + uuid.uuid4().hex[:10]
        command = [sys.executable, str(Path(__file__).with_name('easyproxy_source_audit.py')),
                   '--audit-id', audit_id, '--image', args.image, '--build-if-missing',
                   '--subscription', url, '--output-path', str(summary), '--artifact-dir', tmp,
                   '--scenario-timeout-seconds', str(args.audit_timeout), '--docker-network-name', 'EasyProxyMaintenance']
        try:
            run = subprocess.run(command, capture_output=True, timeout=args.audit_timeout + 1200)
            result = json.loads(summary.read_text(encoding='utf-8')) if summary.exists() else {}
            return classify_audit(result, run.returncode)
        except (OSError, ValueError, subprocess.TimeoutExpired):
            return 'unknown'
        finally:
            # 只清理本次唯一命名的审核容器，不接触生产网关。
            subprocess.run(['docker', 'rm', '-f', 'easyproxy-source-audit-' + audit_id],
                           capture_output=True, check=False)


def probe_source(source, session, args):
    url = source.get('input') or source.get('url')
    outcomes = []
    for attempt in range(2):
        try:
            response = session.get(url, headers={'User-Agent': 'clash.meta'}, timeout=25)
            outcome = classify_response(response)
            if outcome == 'audit':
                outcome = audit_nodes(url, args)
                if outcome == 'unavailable':
                    # 审核宿主机断网时不能把所有订阅一起判死。
                    control = session.get('https://www.gstatic.com/generate_204', timeout=15)
                    if control.status_code != 204:
                        outcome = 'unknown'
        except requests.RequestException:
            outcome = 'unknown'
        outcomes.append(outcome)
        if outcome == 'healthy':
            return 'healthy', outcomes
        if attempt == 0:
            time.sleep(3)
    return ('unavailable' if outcomes == ['unavailable', 'unavailable'] else 'unknown'), outcomes


def build_patch(data, verdicts, remove_ech_profile=False):
    removed, updated, profiles = [], [], []
    for source in data['misubs']:
        if not is_managed(source):
            continue
        verdict = verdicts.get(source['id'])
        if verdict == 'unavailable':
            removed.append(source['id'])
        elif verdict == 'healthy' and source.get('options', {}).get('aggregator_missing') and source.get('enabled') is False:
            updated.append({**copy.deepcopy(source), 'enabled': True})
    retired = []
    for profile in data['profiles']:
        if remove_ech_profile and profile.get('customId') == 'easyproxies-ech-runtime':
            retired.append(profile['id'])
            continue
        changed = copy.deepcopy(profile)
        for key in ('subscriptions', 'manualNodes'):
            if key in changed:
                changed[key] = [item for item in changed[key] if item not in removed]
        if changed != profile:
            profiles.append(changed)
    return {'subscriptions': {'removed': removed, 'updated': updated},
            'profiles': {'removed': retired, 'updated': profiles}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default=os.environ.get('MISUB_PUBLIC_URL') or 'https://misub.aiaimimi.com')
    parser.add_argument('--image', default='easyproxy/discovery-maintenance:current')
    parser.add_argument('--audit-timeout', type=int, default=180)
    parser.add_argument('--work-dir', default=os.environ.get('RUNNER_TEMP'))
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--only-disabled', action='store_true')
    parser.add_argument('--remove-retired-ech-profile', action='store_true')
    args = parser.parse_args()
    base = args.base_url.rstrip('/')
    admin = requests.Session()
    admin.headers['Origin'] = base
    password = os.environ['MISUB_ADMIN_PASSWORD']
    r = admin.post(base + '/api/login', json={'password': password}, timeout=30)
    r.raise_for_status()
    assert r.json().get('success') is True

    def get_data():
        r = admin.get(base + '/api/data', timeout=30); r.raise_for_status(); return r.json()

    data = get_data()
    settings = admin.get(base + '/api/settings', timeout=30); settings.raise_for_status()
    sync = settings.json().get('aggregatorSync', {})
    discovery_url = sync.get('sourceUrl')
    # 外部探测使用独立 Session，绝不转发 MiSub Cookie 或认证头。
    public = requests.Session()
    remote = public.get(discovery_url, timeout=30); remote.raise_for_status()
    remote = remote.json()
    assert isinstance(remote, dict), 'Invalid discovery export; refuse cleanup'
    candidates = [x for x in data['misubs'] if is_managed(x)
                  and (x.get('enabled') is False or (not args.only_disabled and sync.get('autoDisableMissing', True)
                       and (x.get('input') or x.get('url')) not in remote))]
    verdicts, evidence = {}, []
    for source in candidates:
        verdict, checks = probe_source(source, public, args)
        verdicts[source['id']] = verdict
        row = {'id': source['id'], 'verdict': verdict, 'checks': checks}
        evidence.append(row)
        print(json.dumps(row), flush=True)
    patch = build_patch(data, verdicts, args.remove_retired_ech_profile)
    if args.apply and any(patch[k][field] for k in patch for field in patch[k]):
        current = get_data()
        assert current['misubs'] == data['misubs'] and current['profiles'] == data['profiles'], 'Concurrent edit; refuse stale patch'
        # 删除独立 ECH 组之前必须确认 global 仍引用同一个受管 ECH 来源。
        if patch['profiles']['removed']:
            global_profile = next(p for p in data['profiles'] if p.get('customId') == 'aggregator-global')
            assert any(x.startswith('conn_ech_workers_pref_') for x in global_profile.get('manualNodes', []))
        r = admin.post(base + '/api/misubs', json={'diff': patch}, timeout=90)
        r.raise_for_status(); assert r.json().get('success') is True
        after = get_data()
        assert not set(patch['subscriptions']['removed']).intersection(x['id'] for x in after['misubs'])
        assert not set(patch['profiles']['removed']).intersection(x['id'] for x in after['profiles'])
        before_connectors = {x['id']: x for x in data['misubs'] if x.get('kind') == 'connector'}
        assert before_connectors == {x['id']: x for x in after['misubs'] if x.get('kind') == 'connector'}
    print(json.dumps({'applied': args.apply, 'deleted_sources': patch['subscriptions']['removed'],
                      'reactivated_sources': [x['id'] for x in patch['subscriptions']['updated']],
                      'deleted_profiles': patch['profiles']['removed'], 'checked': len(evidence)}))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # 避免 requests/subprocess 异常中的订阅 URL、令牌进入 CI 日志。
        print('MiSub discovery maintenance failed: ' + type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
