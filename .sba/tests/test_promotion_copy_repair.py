"""已尝试晋级但只有首个副本：固定范围、数据摘要、单次移交，旧晋级永不重放。"""
from dataclasses import replace
import hashlib
import io
import json
from pathlib import Path
from types import SimpleNamespace
import sys

import pytest
sys.path.insert(0, str(Path(__file__).parents[1]))
import application
import promotion_copy_repair as repair
import fresh_pristine_config as binding
from coordination import Coordinator, CoordinationError, StoreUnknown, StoreConflict, _encode
from repair_context import PROMOTION_COPY_CONTEXT, PROMOTION_COPY_PARENT_RELEASE, MIGRATION_ACTIVE_RELEASE
from request import RequestError, SECRETS, parse_request
from suite_config import parse_configuration
from test_coordination import MemoryStore, state
from test_aggregator_publish import BODIES
from test_fresh_domain_request import recovery_request as old_request


@pytest.fixture
def approved(monkeypatch):
    value = old_request()
    value.update(applicationVersion='1.0.18', previous=dict(PROMOTION_COPY_PARENT_RELEASE), context=dict(PROMOTION_COPY_CONTEXT))
    config = parse_configuration(value['configuration'], repository=value['repository'], environment=value['environment'])
    monkeypatch.setattr(binding, 'CONFIGURATION_SHA256', hashlib.sha256(_encode({'environment': config.environment, 'configuration': config.configuration()})).hexdigest())
    return value


def parse(value):
    return parse_request(json.dumps(value).encode(), application_version='1.0.18')


@pytest.mark.parametrize('field', list(PROMOTION_COPY_CONTEXT))
def test_exact_parent_context_required(approved, field):
    approved['context'][field] = 42 if field == 'parentRunId' else 'different'
    with pytest.raises(RequestError): parse(approved)


@pytest.fixture
def partial(approved, monkeypatch):
    request = parse(approved); store = MemoryStore()
    store.account_id, store.bucket = request.config.account_id, request.config.bucket
    owner = Coordinator(store, request.config.identity('production'), request.config.inventory_digest)
    initial = owner.begin('deploy', MIGRATION_ACTIVE_RELEASE)
    owner.commit_verified(initial, {'suite-published': True, 'service-ready': True})
    old = owner.begin('update', PROMOTION_COPY_PARENT_RELEASE, previous=MIGRATION_ACTIVE_RELEASE)
    for name, value in {'CONTROL_SHA256': hashlib.sha256(old.state_bytes).hexdigest(), 'CONTROL_ETAG': old.etag,
                        'OPERATION': old.operation_id, 'INVENTORY': request.config.inventory_digest}.items():
        monkeypatch.setattr(repair, name, value)
    backup = b'age-encryption.org/v1\n'.ljust(11878, b'x')
    monkeypatch.setattr(repair, 'BACKUP_SHA256', hashlib.sha256(backup).hexdigest())
    for filename in repair.BASELINE['stages']:
        stage = filename[:-5]
        store.create_stage(old.operation_id, stage, _encode({'operationId': old.operation_id, 'stage': stage, 'permitSha256': repair.CONTROL_SHA256}))
    objects = {f'candidate/{old.operation_id}/{key}': body for key, body in BODIES.items()}
    objects[f'releases/{old.operation_id}/subs/clash.yaml'] = BODIES['subs/clash.yaml']
    objects['last-known-good/manifest.json'] = b'previous'
    objects[f'_sba/backups/{old.operation_id}/misub.age'] = backup
    def rows():
        return {**objects, **{f'_sba/operations/{op}/{stage}.json': body for (op, stage), body in store.stages.items()}}
    def meta(body):
        return {'size': len(body), 'etag': '"' + hashlib.md5(body).hexdigest() + '"'}
    baseline = {label: {key[len(prefix):]: {**meta(body), 'sha256': hashlib.sha256(body).hexdigest()}
                for key, body in rows().items() if key.startswith(prefix)}
                for label, prefix in [('stages', f'_sba/operations/{old.operation_id}/'), ('candidate', f'candidate/{old.operation_id}/')]}
    baseline['lastKnownGoodListingDigest'] = hashlib.sha256(_encode({'manifest.json': meta(b'previous')})).hexdigest()
    monkeypatch.setattr(repair, 'BASELINE', baseline)
    record = {'truncated': False, 'objects': objects, 'calls': []}
    def listing(**kwargs):
        prefix = kwargs['Prefix']
        values = [{'Key': key, 'Size': len(body), 'ETag': meta(body)['etag']} for key, body in rows().items() if key.startswith(prefix)]
        return {'IsTruncated': record['truncated'], 'KeyCount': len(values), 'Contents': values}
    def get(**kwargs):
        return {'Body': io.BytesIO(rows()[kwargs['Key']])}
    store.client = SimpleNamespace(list_objects_v2=listing, get_object=get)
    for symbol in ('verify_parent', 'verify_original_data', 'verify_published', 'verify_provider_baseline'):
        def verify(*_, symbol=symbol, **kwargs):
            record['calls'].append(symbol)
            if symbol == 'verify_provider_baseline': assert kwargs['release'] == PROMOTION_COPY_PARENT_RELEASE
            if record.get('fail') == symbol: raise repair.PromotionCopyRepairError('fixture')
        monkeypatch.setattr(repair, symbol, verify)
    return request, owner, old, store, record


def run(partial):
    request, owner, _, _, _ = partial
    return repair.begin_recovery(owner, request, Path('/fixture'), dict.fromkeys(SECRETS, 'fixture'))


def test_transition_retains_all_old_objects_active_and_stages(partial):
    request, owner, old, store, record = partial
    before = dict(store.stages); objects = dict(record['objects'])
    permit, baseline = run(partial)
    baseline.assert_permit(owner, permit)
    assert baseline.release == PROMOTION_COPY_PARENT_RELEASE and baseline.recovery == 'first-release17'
    assert state(store)['active'] == MIGRATION_ACTIVE_RELEASE and state(store)['writer']['candidate'] == request.candidate
    assert all(store.stages[key] == value for key, value in before.items()) and record['objects'] == objects
    assert len(store.stages) == 8 and len(store.writes) == 4 and permit.operation_id != old.operation_id
    with pytest.raises(repair.PromotionCopyRepairError): run(partial)
    assert len(store.stages) == 8 and len(store.writes) == 4
    with pytest.raises(RuntimeError): replace(baseline, recovery='edge16').assert_permit(owner, permit)


@pytest.mark.parametrize('change', ['etag', 'truncated', 'stage', 'backup', 'candidate', 'release', 'extra-release', 'lkg',
    'verify_parent', 'verify_original_data', 'verify_published', 'verify_provider_baseline'])
def test_any_drift_stops_before_transition_and_keeps_writer(partial, change):
    _, _, old, store, record = partial
    objects = record['objects']
    if change == 'etag': store.etag = '"' + 'f' * 32 + '"'
    elif change == 'truncated': record['truncated'] = True
    elif change == 'stage': store.create_stage(old.operation_id, 'misub-managed-sync', b'{}')
    elif change == 'backup': objects[f'_sba/backups/{old.operation_id}/misub.age'] = b'wrong'
    elif change == 'candidate': objects[f'candidate/{old.operation_id}/subs/clash.yaml'] = b'wrong'
    elif change == 'release': objects[f'releases/{old.operation_id}/subs/clash.yaml'] = b'wrong'
    elif change == 'extra-release': objects[f'releases/{old.operation_id}/subs/mixed.txt'] = b'wrong'
    elif change == 'lkg': objects['last-known-good/manifest.json'] = b'wrong'
    else: record['fail'] = change
    before = (len(store.writes), dict(store.stages))
    with pytest.raises(repair.PromotionCopyRepairError): run(partial)
    assert before == (len(store.writes), store.stages) and state(store)['writer']['operationId'] == old.operation_id


@pytest.mark.parametrize('failure', ['marker-lost', 'cas-unknown', 'cas-lost', 'cas-conflict'])
def test_ambiguous_writes_are_not_replayed_or_unlocked(partial, monkeypatch, failure):
    _, _, _, store, _ = partial
    if failure == 'marker-lost':
        create = store.create_stage
        def lost(*args): create(*args); raise StoreUnknown()
        monkeypatch.setattr(store, 'create_stage', lost)
    elif failure == 'cas-unknown': store.ambiguous = True
    elif failure == 'cas-lost': store.fail_after_commit = True
    else: monkeypatch.setattr(store, 'compare_and_set', lambda *_: (_ for _ in ()).throw(StoreConflict()))
    with pytest.raises((StoreUnknown, StoreConflict, CoordinationError)): run(partial)
    assert state(store)['writer'] is not None and state(store)['active'] == MIGRATION_ACTIVE_RELEASE
    before = (len(store.writes), len(store.stages))
    with pytest.raises(repair.PromotionCopyRepairError): run(partial)
    assert before == (len(store.writes), len(store.stages))


def test_application_uses_old_stable_inputs_and_new_backed_up_update(approved, tmp_path, monkeypatch):
    request = parse(approved); calls = []
    monkeypatch.setattr(application, 'inspect_checkout', lambda *_: [])
    for name in ('prepare_linux_tools', 'build_runtime', 'build_image', 'build_sealer', 'build_cli'):
        monkeypatch.setattr(application, name, lambda *_args, name=name, **_kwargs: calls.append(name))
    def previous(config, release, credentials):
        assert release == MIGRATION_ACTIVE_RELEASE
        calls.append('previous'); return {'old': b'inputs'}
    monkeypatch.setattr(application, 'load_previous_inputs', previous)
    def generate(*_, **kwargs):
        assert kwargs['previous_inputs'] == {'old': b'inputs'}
        calls.append('generation')
    monkeypatch.setattr(application, 'build_aggregator', generate)
    migration = object(); monkeypatch.setattr(application, 'prepare_migration', lambda *_: migration)
    def deploy(current, *_, **kwargs):
        assert current == replace(request, action='update', previous=MIGRATION_ACTIVE_RELEASE, context=None)
        assert kwargs['recovery_request'] is request and kwargs['migration_settings'] is migration
        return {'checks': {'suite-published': True, 'service-ready': True, 'backup-created': True, 'data-preserved': True}}
    monkeypatch.setattr(application, 'deploy_suite', deploy)
    assert application.execute(request, tmp_path, tmp_path.parent / (tmp_path.name + '-out'), environment=dict.fromkeys(SECRETS, 'fixture-value'))['checks']['repair-completed']
    assert calls.index('previous') < calls.index('generation')


def test_historical_manifest_exposes_only_exact_first_copy_recovery():
    manifest = json.loads((Path(__file__).parent / 'fixtures/manifest-1.0.18.json').read_bytes())
    assert manifest['version'] == '1.0.18'
    row, = manifest['repairs']
    assert row['id'] == PROMOTION_COPY_CONTEXT['repairId'] and row['fromErrorCodes'] == [PROMOTION_COPY_CONTEXT['errorCode']]
    assert row['fromActions'] == ['repair'] and set(row['secretNames']) == SECRETS
