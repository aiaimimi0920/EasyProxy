// Bounded streaming JSON for the app-owned managed endpoint.
export async function readBody(request) {
    const maximum = 16 * 1024 * 1024;
    const declared = request.headers.get('Content-Length');
    if (declared !== null && (!/^\d+$/.test(declared) || Number(declared) > maximum)) return null;
    const reader = request.body?.getReader();
    if (!reader) throw new Error('invalid');
    // Copy immediately: tiny chunks must not retain millions of objects/backing buffers.
    const bytes = new Uint8Array(maximum);
    let size = 0;
    try {
        while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            if (value.byteLength > maximum - size) { await reader.cancel(); return null; }
            bytes.set(value, size);
            size += value.byteLength;
        }
    } finally { reader.releaseLock(); }
    return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, size)));
}
