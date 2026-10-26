"""有限Pages子阶段/HTTP分类；故障后单次停写，秘密不进入异常或回执。"""
import json
from pathlib import Path
import re
import sys

import pytest
import requests

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import cloudflare_http as http
import diagnostics
from pages_api import require, PagesError
from test_pages import candidate, api_wire, held, publish, state_writer
from test_pages_asset_response import Session, Response


@pytest.mark.parametrize('status,reason', [(301,'HTTP_3XX'),(400,'HTTP_400'),(401,'HTTP_401'),
    (403,'HTTP_403'),(404,'HTTP_404'),(409,'HTTP_409'),(413,'HTTP_413'),(429,'HTTP_429'),
    (422,'HTTP_4XX'),(500,'HTTP_5XX'),(503,'HTTP_5XX'),(199,'HTTP_OTHER')])
def test_http_metadata_is_finite_and_one_request_only(status, reason):
    session = Session(b'PRIVATE_PROVIDER_BODY_WITH_TOKEN')
    response = Response(session.body)
    response.status_code = status
    calls = []
    def request(*_args, **kwargs):
        calls.append(kwargs)
        return response
    session.request = request
    diagnostic = http.ResponseDiagnostic()
    with pytest.raises(PagesError) as caught:
        http.api_result(session, 'PRIVATE_TOKEN', 'POST', '/pages/assets/upload', require,
            pages_asset_response=True, diagnostic=diagnostic)
    assert diagnostic.reason == reason
    assert len(calls) == 1 and 'diagnostic' not in calls[0]
    assert calls[0]['allow_redirects'] is False and calls[0]['timeout'] == (10, 60)
    assert 'PRIVATE' not in str(caught.value)
    assert not hasattr(diagnostic, '__dict__')


@pytest.mark.parametrize('body,reason', [(b'private-not-json','DECODE'),
    (b'{"success":true,"success":false,"result":[]}', 'DECODE'),
    (b'{"success":false,"errors":[{"code":1000,"message":"PRIVATE_TOKEN"}],"result":null}', 'ENVELOPE'),
    (b'{"success":true}', 'ENVELOPE'), (b'x' * (1024 * 1024 + 1), 'SIZE')],
    ids=['invalid-json', 'duplicate-key', 'provider-error', 'missing-result', 'oversized'])
def test_response_diagnostic_never_copies_body(body, reason):
    session = Session(body)
    diagnostic = http.ResponseDiagnostic()
    with pytest.raises(PagesError):
        http.api_result(session, 'PRIVATE_TOKEN', 'POST', '/pages/assets/check-missing', require,
            pages_asset_response=True, diagnostic=diagnostic)
    assert diagnostic.reason == reason and len(session.calls) == 1


@pytest.mark.parametrize('error,reason', [(requests.Timeout('PRIVATE_TOKEN'), 'TIMEOUT'),
    (requests.ConnectionError('PRIVATE_TOKEN'),'NETWORK'),(requests.RequestException('PRIVATE_TOKEN'),'REQUEST'),
    (OSError('PRIVATE_TOKEN'),'NETWORK')])
def test_network_diagnostic_has_no_retry_or_exception_text(error, reason):
    calls = []
    class Failed:
        def request(self, *_args, **_kwargs):
            calls.append(True)
            raise error
    with pytest.raises(diagnostics.StageError) as caught:
        with diagnostics.pages_phase('PAGES_ASSET_UPLOAD') as diagnostic:
            http.api_result(Failed(), 'PRIVATE_TOKEN', 'POST', '/pages/assets/upload', require,
                pages_asset_response=True, diagnostic=diagnostic)
    assert len(calls) == 1
    assert diagnostics.safe_error_code(caught.value) == 'EASYPROXY_SBA_PAGES_ASSET_UPLOAD_' + reason + '_UNKNOWN'
    assert 'PRIVATE' not in str(caught.value)


def test_diagnostic_success_clears_prior_reason_and_preserves_wire_contract():
    diagnostic = http.ResponseDiagnostic()
    diagnostic.reason = 'HTTP_403'
    session = Session(b'{"success":true,"result":[]}')
    assert http.api_result(session, 'fixture', 'POST', '/pages/assets/check-missing', require,
        pages_asset_response=True, diagnostic=diagnostic) == []
    assert diagnostic.reason is None and len(session.calls) == 1


@pytest.mark.parametrize('stage,reason', [('PAGES_ASSET_UPLOAD','PRIVATE_TOKEN'),('SOURCE','HTTP_403'),
    ('PAGES_ASSET_UPLOAD','http_403'),('PAGES_ASSET_UPLOAD',{'reason':'HTTP_403'})])
def test_untrusted_diagnostic_values_are_rejected(stage, reason):
    with pytest.raises(ValueError, match='EASYPROXY_SBA_DIAGNOSTIC_REJECTED'):
        diagnostics.StageError(stage, reason)


def test_all_diagnostic_codes_fit_actual_springbok_contract():
    for stage in diagnostics.PAGES_STAGES:
        for reason in (*http.RESPONSE_REASONS, None):
            code = diagnostics.safe_error_code(diagnostics.StageError(stage, reason))
            assert re.fullmatch(r'[A-Z][A-Z0-9_]{1,79}', code)
    error = diagnostics.StageError('PAGES_ASSET_UPLOAD', 'HTTP_403')
    error.reason = 'PRIVATE_TOKEN'
    assert diagnostics.safe_error_code(error) == 'EASYPROXY_SBA_FAILED_OR_UNKNOWN'


@pytest.mark.parametrize('mode,code,write_count', [
    ('server-error','EASYPROXY_SBA_PAGES_PROJECT_CREATE_HTTP_5XX_UNKNOWN',1),
    ('asset-upload-denied','EASYPROXY_SBA_PAGES_ASSET_UPLOAD_HTTP_403_UNKNOWN',3),
    ('asset-query-invalid-json','EASYPROXY_SBA_PAGES_ASSET_QUERY_DECODE_UNKNOWN',2),
    ('asset-register-rate-limit','EASYPROXY_SBA_PAGES_ASSET_REGISTER_HTTP_429_UNKNOWN',5)])
def test_real_http_failure_is_precise_and_preserves_locked_writer(candidate, api_wire, mode, code, write_count):
    release, settings = candidate
    api_wire['mode'] = mode
    owner, permit, store, _ = held(release)
    with pytest.raises(diagnostics.StageError) as caught:
        with diagnostics.phase('PUBLISH'):
            with diagnostics.phase('PAGES'):
                publish(owner, permit, settings)
    assert diagnostics.safe_error_code(caught.value) == code
    assert len(api_wire['writes']) == write_count and api_wire['deployments'] == 0
    assert state_writer(store) == permit.operation_id and json.loads(store.body)['active'] is None
    assert 'PRIVATE' not in str(caught.value)
