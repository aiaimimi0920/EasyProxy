"""真实资产查询响应形状回归；兼容只限三个Pages资产端点，不放宽普通provider响应。"""
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from cloudflare_http import api_result
from pages_api import PagesError, require


class Response:
    status_code = 200
    def __init__(self, body): self.body = body
    def __enter__(self): return self
    def __exit__(self, *_): pass
    def iter_content(self, _): yield self.body


class Session:
    def __init__(self, body): self.body, self.calls = body, []
    def request(self, method, url, **kwargs):
        self.calls.append((method, url))
        assert kwargs['allow_redirects'] is False and kwargs['stream'] is True
        return Response(self.body)


@pytest.mark.parametrize('endpoint,result', [('check-missing', ['a' * 32]), ('upload', None), ('upsert-hashes', True)])
def test_sparse_success_only_for_exact_asset_endpoints(endpoint, result):
    session = Session(json.dumps({'success': True, 'result': result}).encode())
    assert api_result(session, 'fixture-token', 'POST', '/pages/assets/' + endpoint, require,
                      pages_asset_response=True) == result
    assert len(session.calls) == 1


@pytest.mark.parametrize('endpoint', ['upload', 'upsert-hashes'])
@pytest.mark.parametrize('explicit_errors', [False, True])
def test_documented_asset_acknowledgement_does_not_require_result(endpoint, explicit_errors):
    payload = {'success': True, 'messages': []}
    if explicit_errors: payload['errors'] = []
    session = Session(json.dumps(payload).encode())
    assert api_result(session, 'fixture-token', 'POST', '/pages/assets/' + endpoint, require,
                      pages_asset_response=True) is None
    assert len(session.calls) == 1


@pytest.mark.parametrize('body', [
    b'{"success":true,"errors":null,"result":[]}',
    b'{"success":true,"errors":{},"result":[]}',
    b'{"success":true,"errors":[{"code":800000}],"result":[]}',
    b'{"success":false,"result":[]}',
    b'{"success":1,"result":[]}',
    b'{"result":[]}',
    b'{"success":true}',
    b'{"success":true,"success":false,"result":[]}',
    b'[]',
])
def test_sparse_does_not_accept_errors_missing_result_or_ambiguous_json(body):
    session = Session(body)
    with pytest.raises(PagesError):
        api_result(session, 'fixture-token', 'POST', '/pages/assets/check-missing', require, pages_asset_response=True)
    assert len(session.calls) == 1


@pytest.mark.parametrize('method,path', [('GET', '/pages/assets/check-missing'), ('POST', '/pages/assets/unknown'),
    ('POST', '/accounts/' + 'c' * 32 + '/pages/projects'), ('PATCH', '/accounts/' + 'c' * 32 + '/pages/projects/fixture'),
    ('PUT', '/accounts/' + 'c' * 32 + '/workers/scripts/fixture')])
def test_sparse_flag_rejects_other_methods_and_provider_endpoints_before_request(method, path):
    session = Session(b'{"success":true,"result":{}}')
    with pytest.raises(PagesError):
        api_result(session, 'fixture-token', method, path, require, pages_asset_response=True)
    assert session.calls == []


@pytest.mark.parametrize('method,path', [('GET', '/accounts/' + 'c' * 32 + '/pages/projects/fixture'),
    ('POST', '/accounts/' + 'c' * 32 + '/pages/projects'), ('POST', '/pages/assets/check-missing')])
def test_default_contract_still_requires_explicit_empty_errors(method, path):
    session = Session(b'{"success":true,"result":{}}')
    with pytest.raises(PagesError): api_result(session, 'fixture-token', method, path, require)
    assert len(session.calls) == 1
