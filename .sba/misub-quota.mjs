// 构建时注入 MiSub；只读业务数据，不把配额变化写成用户手动开关。
import { authMiddleware } from './modules/auth-middleware.js';
import { assertPublicNetworkUrl } from './modules/security-utils.js';
import { parseUsage, quotaEnabled, quotaKey, allowedSource } from './sba-traffic-quota.mjs';

async function readUsage(source, signal) {
    const url = source.input || source.url;
    try {
        signal.throwIfAborted();
        await assertPublicNetworkUrl(url);
        const parsed = new URL(url);
        if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.hash) throw new Error('url');
        const result = await fetch(url, { headers: { 'User-Agent': source.customUserAgent || 'clash.meta',
            'Cache-Control': 'no-cache' }, redirect: 'manual', signal });
        const usage = result.ok ? parseUsage(result.headers.get('subscription-userinfo')) : null;
        await result.body?.cancel();
        return usage ? { status: usage.exhausted ? 'exhausted' : 'available', ...usage } : { status: 'unknown' };
    } catch { return { status: 'unknown' }; }
}

export async function quotaDecisions(env) {
    if (!env.MISUB_DB) throw new Error('storage');
    const result = await env.MISUB_DB.prepare('SELECT data FROM subscriptions').all();
    if (result.success !== true || !Array.isArray(result.results)) throw new Error('read');
    const rows = result.results.map(row => JSON.parse(row.data));
    const guarded = rows.filter(quotaEnabled);
    const decisions = new Map(), rawIds = new Map(), statuses = [];
    for (const source of rows.filter(row => row.kind === 'subscription' && !quotaEnabled(row))) {
        decisions.set(await quotaKey(source.id), source.enabled === true);
    }
    // 所有批次共享总预算；排队期间预算耗尽也返回 unknown，而非拖到客户端超时。
    const signal = AbortSignal.timeout(10000);
    for (let i = 0; i < guarded.length; i += 4) {
        await Promise.all(guarded.slice(i, i + 4).map(async source => {
            const key = await quotaKey(source.id);
            const usage = source.enabled === true ? await readUsage(source, signal) : { status: 'manual-disabled' };
            const allowed = usage.status === 'available';
            decisions.set(key, allowed);
            rawIds.set(source.id, allowed);
            statuses.push({ id: source.id, ...usage, effectiveEnabled: allowed, checkedAt: new Date().toISOString() });
        }));
    }
    return { decisions, rawIds, statuses };
}

export async function quotaStatus({ request, env }) {
    if (request.method !== 'GET') return new Response('Method Not Allowed', { status: 405 });
    if (!await authMiddleware(request, env)) return new Response('Unauthorized', { status: 401 });
    try {
        const { statuses } = await quotaDecisions(env);
        return Response.json({ success: true, sources: statuses }, { headers: { 'Cache-Control': 'no-store' } });
    } catch { return Response.json({ success: false, code: 'QUOTA_READ_UNKNOWN' }, { status: 503 }); }
}

export async function filterQuotaManifest(context) {
    // 上游先完成鉴权；匿名请求不能触发第三方订阅检查。
    const response = await context.next();
    if (response.status !== 200) return response;
    try {
        const value = await response.json();
        if (!Array.isArray(value.sources)) throw new Error('manifest');
        const { decisions, rawIds, statuses } = await quotaDecisions(context.env);
        value.sources = value.sources.filter(source => allowedSource(source, decisions, rawIds));
        value.traffic_quota = statuses;
        const headers = new Headers(response.headers);
        headers.delete('Content-Length');
        headers.set('Cache-Control', 'no-store');
        return Response.json(value, { headers });
    } catch { return Response.json({ success: false, code: 'QUOTA_MANIFEST_UNKNOWN' }, { status: 503 }); }
}
