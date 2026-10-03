import importlib.util
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('maintenance', Path(__file__).parents[1] / 'scripts/maintain-misub-discovery.py')
maintenance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(maintenance)


def source(id='discovery', managed='aggregator_sync'):
    return {'id': id, 'kind': 'subscription', 'enabled': False, 'options': {'managed_by': managed, 'aggregator_missing': True}}


def test_missing_and_disabled_alone_never_delete():
    data = {'misubs': [source()], 'profiles': []}
    assert maintenance.build_patch(data, {})['subscriptions']['removed'] == []
    assert maintenance.build_patch(data, {'discovery': 'unknown'})['subscriptions']['removed'] == []
    updated = maintenance.build_patch(data, {'discovery': 'healthy'})['subscriptions']['updated']
    assert updated[0]['enabled'] is True


def test_deletion_is_scoped_and_cleans_references_not_connector_sources():
    data = {'misubs': [source(), source('manual', 'manual'), {'id': 'ech', 'kind': 'connector'}],
            'profiles': [{'id': 'global', 'customId': 'aggregator-global', 'subscriptions': ['discovery', 'manual'], 'manualNodes': ['ech']},
                         {'id': 'retired', 'customId': 'easyproxies-ech-runtime', 'manualNodes': ['ech']}]}
    diff = maintenance.build_patch(data, {'discovery': 'unavailable', 'manual': 'unavailable', 'ech': 'unavailable'}, True)
    assert diff['subscriptions']['removed'] == ['discovery']
    assert diff['profiles']['removed'] == ['retired']
    assert diff['profiles']['updated'][0]['subscriptions'] == ['manual']
    assert diff['profiles']['updated'][0]['manualNodes'] == ['ech']
    assert data['profiles'][0]['subscriptions'] == ['discovery', 'manual']


def test_response_200_is_not_node_availability_and_challenges_are_unknown():
    for status in (403, 429, 500):
        assert maintenance.classify_response(SimpleNamespace(status_code=status, text='')) == 'unknown'
    assert maintenance.classify_response(SimpleNamespace(status_code=410, text='')) == 'unavailable'
    assert maintenance.classify_response(SimpleNamespace(status_code=200, text='<!doctype html>landing page')) == 'unavailable'
    assert maintenance.classify_response(SimpleNamespace(status_code=200, text='<html>Just a moment...')) == 'unknown'
    assert maintenance.classify_response(SimpleNamespace(status_code=200, text='ss://node')) == 'audit'


def test_runtime_failure_must_not_be_infrastructure_failure():
    assert maintenance.classify_audit({'error': 'docker failed'}, 1) == 'unknown'
    assert maintenance.classify_audit({'nodes': {'stable_available_uris': ['ss://test']}}, 0) == 'healthy'
    failed = {'nodes': {'total_nodes': 3, 'available_nodes': 0}, 'error': 'proxy lease output failed across all shared probe targets'}
    assert maintenance.classify_audit(failed, 1) == 'unknown'
    failed['pool_probe'] = {'attempts': [{'exit_code': 7, 'stderr': 'URLError'}]}
    assert maintenance.classify_audit(failed, 1) == 'unknown'
    assert maintenance.classify_audit(failed, 2) == 'unavailable'


def test_delete_requires_two_failures_and_any_healthy_result_preserves():
    session = Mock()
    session.get.return_value = SimpleNamespace(status_code=200, text='ss://node')
    with patch.object(maintenance, 'audit_nodes', side_effect=['unavailable', 'healthy']), patch.object(maintenance.time, 'sleep'):
        assert maintenance.probe_source({'url': 'https://example/sub'}, session, None)[0] == 'healthy'
    with patch.object(maintenance, 'audit_nodes', side_effect=['unavailable', 'unknown']), patch.object(maintenance.time, 'sleep'):
        assert maintenance.probe_source({'url': 'https://example/sub'}, session, None)[0] == 'unknown'
    session.get.return_value = SimpleNamespace(status_code=404, text='')
    with patch.object(maintenance.time, 'sleep'):
        assert maintenance.probe_source({'url': 'https://example/sub'}, session, None)[0] == 'unavailable'
