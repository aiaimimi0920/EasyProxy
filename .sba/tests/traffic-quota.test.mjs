import test from 'node:test';
import assert from 'node:assert/strict';
import { parseUsage, quotaKey, uriQuotaKeys, allowedSource } from '../traffic-quota.mjs';

test('strictly greater than 99 percent; upload counts; exact large integers', () => {
    assert.equal(parseUsage('upload=0; download=99; total=100').exhausted, false);
    assert.equal(parseUsage('upload=1; download=99; total=100').exhausted, true);
    assert.equal(parseUsage('upload=0; download=99000000000000000001; total=100000000000000000000').exhausted, true);
});
test('unknown quota is not zero usage', () => {
    for (const value of [null, '', 'upload=0;download=0', 'upload=0;download=0;total=0',
        'upload=-1;download=0;total=100', 'upload=0;upload=1;download=0;total=100']) assert.equal(parseUsage(value), null);
});
test('refill makes source available without changing the manual switch', () => {
    assert.equal(parseUsage('upload=0;download=100;total=100').exhausted, true);
    assert.equal(parseUsage('upload=0;download=1;total=100').exhausted, false);
});
test('old published nodes are excluded independently of pool publication', async () => {
    const key = await quotaKey('fixture-source');
    const source = { id: 'proxy_runtime_node_1', kind: 'proxy_uri',
        options: { managed_by: 'easyproxy_runtime_sources' }, input: 'ss://fixture#' + encodeURIComponent(`[epq:${key}] node`) };
    assert.deepEqual(uriQuotaKeys(source.input), [key]);
    assert.equal(allowedSource(source, new Map([[key, false]]), new Map()), false);
    assert.equal(allowedSource(source, new Map(), new Map()), false);
    assert.equal(allowedSource(source, new Map([[key, true]]), new Map()), true);
    assert.equal(allowedSource({ id: 'manual', input: 'ss://fixture#ordinary' }, new Map(), new Map()), true);
    assert.equal(allowedSource({ id: 'raw' }, new Map(), new Map([['raw', false]])), false);
    assert.equal(allowedSource({ ...source, id: 'manual' }, new Map(), new Map()), true);
    assert.equal(allowedSource({ ...source, options: {} }, new Map(), new Map()), true);
});
