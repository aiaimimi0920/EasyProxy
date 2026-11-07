"""准确 1.0.17 首个不可变副本后失败的单次移交；不继续或重放旧晋级。"""
import hashlib
import uuid

from aggregator_publish import PublicArtifacts, RAW, _previous, _validate
from artifact_store import ArtifactStore
from cloudflare_http import decode_object, secure_session
from coordination import Coordinator, Permit, ETAG, _encode
from d1_init import D1API
from edge_repair import verify_provider_baseline
from migration_repair import verify_d1, STABLE_SHA256, STABLE_ETAG
from promotion_copy_baseline import BASELINE
from publication_baseline import PublicationBaseline
from repair_context import PROMOTION_COPY_CONTEXT, PROMOTION_COPY_PARENT_RELEASE, MIGRATION_ACTIVE_RELEASE
from request import ApplicationRequest
from worker_api import public_release

OPERATION = '32ca11b37c6c4e1283c0d5ee8205d0bf'
CONTROL_SHA256 = '5a68adaea59fe5b7006847c4e3a39aeadf72a81ccbb91d15daea5305a690912e'
CONTROL_ETAG = '"602dabdb46976349d7165a7cc5ac12f0"'
INVENTORY = '79962304889e05df2e52433195c6cd8659d9481c3a6d0a175a69170ed754aabb'
BACKUP_SHA256 = 'b5d5d6614e63fe8c020b8ccacd436d7d53fe50653c937b00e5cc08d497620e76'
TRANSITION = 'update-first-release-recovery-transition'


class PromotionCopyRepairError(RuntimeError):
    pass


def require(value, *_):
    if not value:
        raise PromotionCopyRepairError('EASYPROXY_SBA_PROMOTION_COPY_REPAIR_REJECTED')


def checked_state(owner):
    raw, etag = owner.store.read()
    require(hashlib.sha256(raw).hexdigest() == CONTROL_SHA256 and etag == CONTROL_ETAG)
    value, observed = owner._read()
    require(_encode(value) == raw and observed == etag and value['active'] == MIGRATION_ACTIVE_RELEASE
            and value['writer'] == {'action': 'update', 'candidate': PROMOTION_COPY_PARENT_RELEASE, 'operationId': OPERATION})
    return value, etag


def checked_objects(owner, *, transition=None):
    client, bucket = owner.store.client, owner.store.bucket
    def listing(prefix):
        value = client.list_objects_v2(Bucket=bucket, Prefix=prefix, MaxKeys=1000)
        rows = value.get('Contents', [])
        require(value.get('IsTruncated') is False and value.get('KeyCount') == len(rows))
        return {row['Key'][len(prefix):]: {'size': row['Size'], 'etag': row['ETag']} for row in rows}
    def read(key, limit):
        response = client.get_object(Bucket=bucket, Key=key)
        try:
            raw = response['Body'].read(limit + 1)
            require(len(raw) <= limit)
            return raw
        finally:
            response['Body'].close()
    backup = read(f'_sba/backups/{OPERATION}/misub.age', 11878)
    require(len(backup) == 11878 and backup.startswith(b'age-encryption.org/v1\n')
            and hashlib.sha256(backup).hexdigest() == BACKUP_SHA256)
    for label, prefix in [('stages', f'_sba/operations/{OPERATION}/'), ('candidate', f'candidate/{OPERATION}/')]:
        expected = {key: {'size': row['size'], 'etag': row['etag']} for key, row in BASELINE[label].items()}
        if label == 'stages' and transition is not None:
            observed = listing(prefix)
            extra = observed.pop(TRANSITION + '.json', None)
            require(extra is not None and read(prefix + TRANSITION + '.json', 16384) == transition)
        else:
            observed = listing(prefix)
        require(observed == expected)
        for key, row in BASELINE[label].items():
            require(hashlib.sha256(read(prefix + key, row['size'])).hexdigest() == row['sha256'])
    release = BASELINE['candidate']['subs/clash.yaml']
    require(listing(f'releases/{OPERATION}/') == {'subs/clash.yaml': {'size': release['size'], 'etag': release['etag']}})
    require(hashlib.sha256(read(f'releases/{OPERATION}/subs/clash.yaml', release['size'])).hexdigest() == release['sha256'])
    require(hashlib.sha256(_encode(listing('last-known-good/'))).hexdigest() == BASELINE['lastKnownGoodListingDigest'])


def verify_parent(token):
    session = secure_session()
    try:
        def read(url):
            with session.get(url, headers={'Authorization': 'Bearer ' + token}, timeout=(10, 40), stream=True, allow_redirects=False) as response:
                require(response.status_code == 200)
                raw = bytearray()
                for chunk in response.iter_content(65536):
                    raw.extend(chunk)
                    require(len(raw) <= 1048576)
                return decode_object(bytes(raw), require)
        base = 'https://api.github.com/repos/aiaimimi0920/SpringBok/actions/runs/42379246256'
        run = read(base)
        require(run.get('id') == PROMOTION_COPY_CONTEXT['parentRunId'] and type(run.get('run_attempt')) is int
                and run['run_attempt'] == 1 and run.get('status') == 'completed' and run.get('conclusion') == 'success'
                and run.get('head_sha') == 'de26192f0be5c1f4df23e82e3ac3104ab59bcf30'
                and run.get('event') == 'workflow_dispatch' and run.get('path') == '.github/workflows/sba-execute-linux.yml'
                and run.get('display_title') == 'sba:' + PROMOTION_COPY_CONTEXT['parentTaskId'] + ':' + PROMOTION_COPY_CONTEXT['requestDigest'])
        jobs = read(base + '/attempts/1/jobs?per_page=100')
        require(jobs.get('total_count') == 1 and type(jobs.get('jobs')) is list and len(jobs['jobs']) == 1)
        job = jobs['jobs'][0]
        require(job.get('id') == 128423177888 and job.get('status') == 'completed' and job.get('conclusion') == 'success'
                and job.get('labels') == ['ubuntu-24.04'] and job.get('runner_group_name') == 'GitHub Actions'
                and type(job.get('completed_at')) is str)
        for status in ('queued', 'in_progress', 'waiting', 'pending', 'requested'):
            runs = read('https://api.github.com/repos/aiaimimi0920/EasyProxy/actions/workflows/sba-aggregator-refresh.yml/runs?per_page=100&status=' + status)
            require(runs.get('total_count') == 0 and runs.get('workflow_runs') == [])
    finally:
        session.close()


def verify_original_data(config, root, token):
    verify_d1(config, root, token)
    api = D1API(config.account_id, token, config.database_id)
    try:
        data = {table: api.query('SELECT * FROM ' + table + ' ORDER BY ' + key) for table, key in
                [('subscriptions', 'id'), ('profiles', 'id'), ('settings', 'key'), ('cron_executions', 'id')]}
        require(hashlib.sha256(_encode(data)).hexdigest() == BASELINE['d1Sha256'])
    finally:
        api.close()


def verify_published(owner, config):
    expected = {'schemaVersion': 1, 'applicationId': 'easyproxy-cloud', **PROMOTION_COPY_PARENT_RELEASE}
    require(public_release(config.worker_origin) == public_release(config.pages_origin) == expected)
    public = PublicArtifacts(config.worker_origin + '/aggregator', inventory=config.inventory)
    try:
        artifacts = ArtifactStore(owner.store)
        prior, _, verified = _previous(artifacts, public, MIGRATION_ACTIVE_RELEASE)
        require(prior is not None and hashlib.sha256(prior.body).hexdigest() == STABLE_SHA256
                and prior.etag == STABLE_ETAG and len(verified) == 7)
        bodies = {}
        for key in RAW:
            obj = artifacts.get(f'candidate/{OPERATION}/{key}')
            require(obj is not None and hashlib.sha256(obj.body).hexdigest() == BASELINE['candidate'][key]['sha256'])
            bodies[key] = obj.body
            public.verify(f'candidate/{OPERATION}/{key}', obj.body)
        _validate(bodies, RAW)
    finally:
        public.close()


def inspect_recovery(owner: Coordinator, request: ApplicationRequest, root, credentials):
    require(type(request) is ApplicationRequest and request.action == 'repair' and request.context == PROMOTION_COPY_CONTEXT
            and request.previous == PROMOTION_COPY_PARENT_RELEASE and request.application_version == '1.0.18'
            and owner.identity == request.config.identity('production') and owner.inventory_digest == INVENTORY
            and owner.inventory_digest == request.config.inventory_digest)
    value, etag = checked_state(owner)
    checked_objects(owner)
    verify_parent(credentials['EASYPROXY_GITHUB_TOKEN'])
    verify_original_data(request.config, root, credentials['EASYPROXY_CLOUDFLARE_API_TOKEN'])
    verify_published(owner, request.config)
    verify_provider_baseline(owner, request.config, credentials, release=PROMOTION_COPY_PARENT_RELEASE)
    require(checked_state(owner) == (value, etag))
    checked_objects(owner)
    return value, etag


def begin_recovery(owner, request, root, credentials):
    value, etag = inspect_recovery(owner, request, root, credentials)
    operation = uuid.uuid4().hex
    evidence = _encode({'schemaVersion': 1, 'context': request.context, 'taskId': request.task_id,
        'previousControlSha256': CONTROL_SHA256, 'previousOperationId': OPERATION,
        'active': MIGRATION_ACTIVE_RELEASE, 'candidate': request.candidate, 'operationId': operation})
    marker = owner.store.create_stage(OPERATION, TRANSITION, evidence)
    require(type(marker) is str and ETAG.fullmatch(marker) is not None)
    checked_objects(owner, transition=evidence)
    require(checked_state(owner) == (value, etag))
    value['revision'] = uuid.uuid4().hex
    value['writer'] = {'operationId': operation, 'action': 'update', 'candidate': request.candidate}
    raw, new_etag = owner._write(value, etag)
    permit = Permit(operation, 'update', new_etag, raw)
    owner.assert_owned(permit)
    return permit, PublicationBaseline(operation, hashlib.sha256(raw).hexdigest(), 'first-release17')
