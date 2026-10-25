import base64
import importlib.util
import io
import json
from pathlib import Path
from unittest.mock import Mock

import pytest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('publish_v2ray', ROOT / 'scripts/publish-aggregator-v2ray.py')
publisher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(publisher)


def runtime():
    return {'storage': {'items': {
        'public-mixed': {'bucket': 'test', 'key': 'candidate/run-1/subs/mixed.txt'},
        'public-v2ray': {'bucket': 'test', 'key': 'candidate/run-1/subs/v2ray.txt'},
    }}}


def test_no_vmess_still_produces_compatible_subscription():
    # 线上失败候选有 SS/VLESS/Trojan，但没有 VMess。
    lines = b'ss://synthetic\nvless://synthetic\ntrojan://synthetic\n'
    body, summary = publisher.build_payload(base64.b64encode(lines))
    assert base64.b64decode(body) == lines
    assert summary['count'] == 3
    assert summary['schemes'] == {'ss': 1, 'vless': 1, 'trojan': 1}


def test_filter_preserves_supported_protocols_and_order():
    body, summary = publisher.build_payload(base64.b64encode(
        b'vmess://synthetic\nssr://synthetic\nhysteria2://synthetic\nvmess://synthetic\n'))
    assert base64.b64decode(body) == b'vmess://synthetic\nssr://synthetic\n'
    assert summary['count'] == 2
    assert summary['skipped'] == 1


@pytest.mark.parametrize('content', [b'', b'not base64!', base64.b64encode(b'<html>ss://fake</html>'),
                                     base64.b64encode(b'ss://'), base64.b64encode(b'\xff'),
                                     base64.b64encode(b'hysteria2://synthetic')])
def test_bad_or_incompatible_content_fails_closed(content):
    client = Mock()
    client.get_object.return_value = {'Body': io.BytesIO(content)}
    with pytest.raises(RuntimeError):
        publisher.publish(client, runtime())
    client.put_object.assert_not_called()


def test_size_limit_rejects_before_publish(monkeypatch):
    monkeypatch.setattr(publisher, 'MAX_BODY_BYTES', 4)
    with pytest.raises(RuntimeError, match='size limit'):
        publisher.build_payload(b'12345')


@pytest.mark.parametrize('key', ['subs/v2ray.txt', 'candidate/subs/v2ray.txt',
                                 'releases/run-1/subs/v2ray.txt', 'candidate/other/subs/v2ray.txt',
                                 'candidate/run-1/../v2ray.txt', 'candidate/run-1//v2ray.txt',
                                 'candidate/run-1/subs/mixed.txt'])
def test_refuses_stable_alias_different_run_or_unsafe_destination(key):
    config = runtime()
    config['storage']['items']['public-v2ray']['key'] = key
    client = Mock()
    with pytest.raises(RuntimeError):
        publisher.publish(client, config)
    client.get_object.assert_not_called()
    client.put_object.assert_not_called()


def test_refuses_different_bucket():
    config = runtime()
    config['storage']['items']['public-v2ray']['bucket'] = 'other'
    with pytest.raises(RuntimeError):
        publisher.candidate_items(config)


def test_publish_reads_and_writes_only_current_candidate():
    client = Mock()
    stream = io.BytesIO(base64.b64encode(b'ss://synthetic\n'))
    client.get_object.return_value = {'Body': stream}
    summary = publisher.publish(client, runtime())
    assert summary['count'] == 1
    assert stream.closed
    client.get_object.assert_called_once_with(Bucket='test', Key='candidate/run-1/subs/mixed.txt')
    assert client.put_object.call_args.kwargs['Key'] == 'candidate/run-1/subs/v2ray.txt'
    assert base64.b64decode(client.put_object.call_args.kwargs['Body']) == b'ss://synthetic\n'


def test_generation_precedes_strict_gate_and_has_single_owner():
    config = json.loads((ROOT / 'deploy/upstreams/aggregator/config/config.actions.r2.json').read_text(encoding='utf-8'))
    assert 'v2ray' not in config['groups']['public']['targets']
    assert 'public-v2ray' in config['storage']['items']
    workflow = (ROOT / '.github/workflows/deploy-aggregator.yml').read_text(encoding='utf-8')
    assert workflow.index('python scripts/publish-aggregator-v2ray.py') < workflow.index('--before-effective-audit')
    assert workflow.index('--before-effective-audit') < workflow.index('- name: Audit shared effective aggregator nodes')
