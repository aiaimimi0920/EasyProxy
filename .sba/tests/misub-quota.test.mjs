import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { quotaKey } from '../traffic-quota.mjs';

// 生产模块原样执行；仅替换认证依赖，D1/fetch 是显式离线边界。
const code = (await readFile(new URL('../misub-quota.mjs', import.meta.url), 'utf8'))
    .replace("import { authMiddleware } from './modules/auth-middleware.js';", 'const authMiddleware = async request => request.headers.get("Cookie") === "fixture-auth";')
    .replace("'./modules/security-utils.js'", JSON.stringify(new URL('../../upstreams/misub/functions/modules/security-utils.js', import.meta.url).href))
    .replace("'./sba-traffic-quota.mjs'", JSON.stringify(new URL('../traffic-quota.mjs', import.meta.url).href));
const { quotaDecisions, filterQuotaManifest, quotaStatus } = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'));
const source = { id: 'quota-source', kind: 'subscription', enabled: true,
    input: 'https://fixture.example/sub', options: { trafficQuota: { enabled: true } } };
function environment(rows) {
    return { MISUB_DB: { prepare(sql) {
        assert.equal(sql, 'SELECT data FROM subscriptions');
        return { all: async () => ({ success: true, results: rows.map(row => ({ data: JSON.stringify(row) })) }) };
    } } };
}
async function node() {
    return { id: 'proxy_runtime_node_1', kind: 'proxy_uri', options: { managed_by: 'easyproxy_runtime_sources' },
        input: 'ss://fixture#' + encodeURIComponent(`[epq:${await quotaKey(source.id)}] 中文 + name`) };
}
test('manifest filters exhausted/unknown, restores reset, and never changes persisted enabled', async t => {
    const managed = await node(), rows = [structuredClone(source)], before = JSON.stringify(rows);
    let header = 'upload=0; download=100; total=100';
    t.mock.method(globalThis, 'fetch', async (_url, options) => {
        assert.equal(options.redirect, 'error');
        assert.equal(options.headers.Authorization, undefined);
        return new Response('', { headers: header ? { 'subscription-userinfo': header } : {} });
    });
    const context = { env: environment(rows), next: async () => Response.json({ success: true, sources: [managed,
        { ...managed, id: 'manual' }, { id: 'connector', kind: 'connector' }] }) };
    for (const [value, allowed] of [[header, false], [null, false], ['upload=0;download=1;total=100', true],
        ['upload=0;download=99;total=100', true]]) {
        header = value;
        const response = await filterQuotaManifest(context);
        assert.equal(response.status, 200);
        assert.equal(response.headers.get('Cache-Control'), 'no-store');
        const data = await response.json();
        assert.equal(data.sources.some(row => row.id === managed.id), allowed);
        assert.ok(data.sources.some(row => row.id === 'manual'));
        assert.ok(data.sources.some(row => row.id === 'connector'));
    }
    assert.equal(JSON.stringify(rows), before);
});
test('manual disabled never fetches, turning quota off permits old marker, deleted source stays blocked', async t => {
    t.mock.method(globalThis, 'fetch', async () => { throw new Error('must not fetch'); });
    const managed = await node();
    for (const [rows, allowed] of [[[ { ...source, enabled: false } ], false],
        [[ { ...source, options: {} } ], true], [[], false]]) {
        const response = await filterQuotaManifest({ env: environment(rows), next: async () => Response.json({ sources: [managed] }) });
        assert.equal((await response.json()).sources.length, allowed ? 1 : 0);
    }
});
test('all batches share one deadline and budget exhaustion returns unknown for queued sources', async t => {
    let deadlines = 0, fetches = 0;
    t.mock.method(AbortSignal, 'timeout', ms => { assert.equal(ms, 10000); deadlines++; return AbortSignal.abort(); });
    t.mock.method(globalThis, 'fetch', async () => { fetches++; throw new Error('must not fetch'); });
    const result = await quotaDecisions(environment(Array.from({ length: 9 }, (_, i) => ({ ...source, id: `quota-${i}` }))));
    assert.equal(result.statuses.length, 9);
    assert.ok(result.statuses.every(row => row.status === 'unknown' && !row.effectiveEnabled));
    assert.equal(deadlines, 1);
    assert.equal(fetches, 0);
});
test('unauthenticated manifest and admin endpoint never probe quota', async () => {
    const env = { get MISUB_DB() { throw new Error('must not read'); } };
    const response = await filterQuotaManifest({ env, next: async () => new Response('Unauthorized', { status: 401 }) });
    assert.equal(response.status, 401);
    assert.equal((await quotaStatus({ env, request: new Request('https://misub.invalid/api/sba/quota-status') })).status, 401);
});
