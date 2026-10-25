import assert from 'node:assert/strict';
import test from 'node:test';
import { readBody } from '../misub-body.mjs';

test('valid chunked UTF-8 body is decoded only after bounded streaming', async () => {
    const bytes = new TextEncoder().encode(JSON.stringify({ label: '保留设置' }));
    let offset = 0;
    const request = new Request('https://fixture.invalid', { method: 'POST', duplex: 'half',
        body: new ReadableStream({ pull(controller) {
            if (offset === bytes.length) return controller.close();
            controller.enqueue(bytes.slice(offset, ++offset));
        } }) });
    assert.equal(request.headers.get('Content-Length'), null);
    assert.deepEqual(await readBody(request), { label: '保留设置' });
});

test('oversized chunked input is canceled without buffering the remaining stream', async () => {
    let pulled = 0, canceled = false;
    const request = new Request('https://fixture.invalid', { method: 'POST', duplex: 'half',
        body: new ReadableStream({ pull(controller) {
            pulled++;
            controller.enqueue(new Uint8Array(1024 * 1024));
            assert.ok(pulled < 25, 'reader must not buffer the entire unbounded stream');
        }, cancel() { canceled = true; } }, { highWaterMark: 0 }) });
    assert.equal(request.headers.get('Content-Length'), null);
    assert.equal(await readBody(request), null);
    assert.equal(canceled, true);
    assert.equal(pulled, 17);
});

test('oversized declared length is rejected before reading', async () => {
    let pulled = false;
    const request = new Request('https://fixture.invalid', { method: 'POST', duplex: 'half',
        headers: { 'Content-Length': String(16 * 1024 * 1024 + 1) },
        body: new ReadableStream({ pull() { pulled = true; } }, { highWaterMark: 0 }) });
    assert.equal(await readBody(request), null);
    assert.equal(pulled, false);
});

test('invalid UTF-8 and malformed JSON never produce accepted objects', async () => {
    for (const body of [new Uint8Array([255]), '{invalid']) {
        await assert.rejects(readBody(new Request('https://fixture.invalid', { method: 'POST', body })));
    }
});

test('tiny chunks are copied before the next read without retaining input objects', async () => {
    const bytes = new TextEncoder().encode(JSON.stringify({ value: 'x'.repeat(131072) }));
    const reused = new Uint8Array(1);
    let offset = 0, released = false;
    const request = { headers: new Headers(), body: { getReader: () => ({
        async read() {
            if (offset === bytes.length) return { done: true };
            reused[0] = bytes[offset++];
            return { done: false, value: reused };
        }, releaseLock() { released = true; },
    }) } };
    assert.deepEqual(await readBody(request), { value: 'x'.repeat(131072) });
    assert.equal(released, true);
});
