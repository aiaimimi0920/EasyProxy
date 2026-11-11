// 配额是独立的有效性条件，不覆盖用户的 enabled 开关。
export function parseUsage(header) {
    if (typeof header !== 'string' || header.length > 4096) return null;
    const fields = {};
    for (const part of header.split(';')) {
        const match = /^\s*(upload|download|total|expire)\s*=\s*(\d+)\s*$/.exec(part);
        if (!match) {
            if (/^\s*(upload|download|total|expire)\s*=/.test(part)) return null;
            continue;
        }
        if (Object.hasOwn(fields, match[1])) return null;
        fields[match[1]] = BigInt(match[2]);
    }
    if (fields.upload === undefined || fields.download === undefined || !fields.total) return null;
    const used = fields.upload + fields.download;
    return { upload: String(fields.upload), download: String(fields.download), total: String(fields.total),
        exhausted: used * 100n > fields.total * 99n };
}

export function quotaEnabled(source) {
    return source?.kind === 'subscription' && source.options?.trafficQuota?.enabled === true;
}

export async function quotaKey(id) {
    const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(id));
    return [...new Uint8Array(bytes)].map(n => n.toString(16).padStart(2, '0')).join('');
}

export function uriQuotaKeys(uri) {
    if (typeof uri !== 'string' || !uri.includes('#')) return [];
    let name;
    try { name = decodeURIComponent(uri.slice(uri.indexOf('#') + 1).replace(/\+/g, ' ')); }
    catch { return []; }
    const match = /^\[epq:([a-f0-9]{64}(?:,[a-f0-9]{64})*)\]/.exec(name);
    return match ? match[1].split(',') : [];
}

export function allowedSource(source, decisions, rawIds) {
    if (rawIds.has(source.id) && !rawIds.get(source.id)) return false;
    if (source.kind !== 'proxy_uri' || !/^proxy_runtime_node_\d+$/.test(source.id) ||
        source.options?.managed_by !== 'easyproxy_runtime_sources') return true;
    return uriQuotaKeys(source.input || source.url).every(key => decisions.get(key) === true);
}
