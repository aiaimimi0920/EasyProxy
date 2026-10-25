// Copied beside the approved MiSub Functions router, never into its repository.
import { authMiddleware } from './modules/auth-middleware.js';
import { StorageFactory } from './storage-adapter.js';
import { canonicalJSONStringify } from '../src/shared/backup-format.js';
import { readBody } from './sba-body.mjs';

const profileId = 'aggregator-global';
const managed = id => typeof id === 'string' && /^(proxy_runtime_node|conn_ech_workers_pref)_\d+$/.test(id);
const response = (status, code) => Response.json({ success: status === 200, code }, {
    status, headers: { 'Cache-Control': 'no-store' },
});
const same = (left, right) => canonicalJSONStringify(left) === canonicalJSONStringify(right);
const sorted = rows => [...rows].sort((a, b) => a.id.localeCompare(b.id));

function collection(rows) {
    if (!Array.isArray(rows) || rows.length > 100000 || rows.some(row => !row || typeof row !== 'object' ||
        Array.isArray(row) || typeof row.id !== 'string' || !row.id || row.id === 'main') ||
        new Set(rows.map(row => row.id)).size !== rows.length) throw new Error('invalid');
    return rows;
}

function changes(value) {
    if (!value || Object.keys(value).sort().join(',') !== 'added,removed,updated' ||
        !Array.isArray(value.removed) || value.removed.some(id => !managed(id))) throw new Error('invalid');
    const added = collection(value.added), updated = collection(value.updated);
    const ids = [...added, ...updated].map(row => row.id).concat(value.removed);
    if (new Set(ids).size !== ids.length) throw new Error('invalid');
    return { added, updated, removed: value.removed };
}

export async function managedSync({ request, env }) {
    if (request.method !== 'POST') return response(405, 'METHOD');
    if (request.headers.get('Origin') !== new URL(request.url).origin) return response(403, 'ORIGIN');
    if (!await authMiddleware(request, env)) return response(401, 'AUTH');
    if (!env.MISUB_DB) return response(503, 'STORAGE');
    try {
        const body = await readBody(request);
        if (body === null) return response(413, 'SIZE');
        if (Object.keys(body).sort().join(',') !== 'before,diff,operationId,sourceSha' ||
            !/^[a-f0-9]{32}$/.test(body.operationId) || body.sourceSha !== env.SBA_RELEASE_SHA ||
            Object.keys(body.before).sort().join(',') !== 'profiles,subscriptions' ||
            Object.keys(body.diff).sort().join(',') !== 'profiles,subscriptions') return response(400, 'INPUT');
        const before = { subscriptions: collection(body.before.subscriptions), profiles: collection(body.before.profiles) };
        const subscriptions = changes(body.diff.subscriptions), profiles = changes(body.diff.profiles);
        const allSources = [...subscriptions.added, ...subscriptions.updated];
        if (allSources.some(row => !managed(row.id) || !['proxy_uri', 'connector'].includes(row.kind) || row.enabled !== true ||
            typeof row.input !== 'string' || !row.input || row.input !== row.url)) return response(400, 'SCOPE');
        if (profiles.removed.length || profiles.added.length + profiles.updated.length !== 1) return response(400, 'SCOPE');
        const target = [...profiles.added, ...profiles.updated][0];
        if (target.customId !== profileId || target.enabled !== true || !Array.isArray(target.manualNodes) ||
            !Array.isArray(target.subscriptions) || target.subscriptions.length) return response(400, 'SCOPE');
        const existing = before.profiles.filter(row => row.id === target.id || row.customId === profileId);
        if (existing.length > 1 || existing.length === 1 && (existing[0].id !== target.id ||
            existing[0].customId !== profileId || !profiles.updated.length) || !existing.length && !profiles.added.length) return response(400, 'SCOPE');
        for (const [table, diff] of [['subscriptions', subscriptions], ['profiles', profiles]]) {
            const current = await env.MISUB_DB.prepare(`SELECT data FROM ${table}`).all();
            if (current.success !== true || !Array.isArray(current.results)) return response(503, 'READ_UNKNOWN');
            const rows = collection(current.results.map(row => JSON.parse(row.data)));
            if (!same(sorted(rows), sorted(before[table]))) return response(409, 'CONCURRENT_EDIT');
            const ids = new Set(rows.map(row => row.id));
            if (diff.added.some(row => ids.has(row.id)) || diff.updated.some(row => !ids.has(row.id)) ||
                diff.removed.some(id => !ids.has(id))) return response(409, 'CONCURRENT_EDIT');
        }
        const storage = StorageFactory.createAdapter(env, 'd1');
        // Deliberately bypass handleMisubsSave: it probes every existing source,
        // launches notifications and clears unrelated caches even for a diff.
        // Multiple D1 operations are not an atomic compare-and-swap transaction.
        // A failure leaves partial state; the caller retains its durable writer.
        for (const row of allSources) await storage.putSubscription(row);
        for (const id of subscriptions.removed) await storage.deleteSubscriptionById(id);
        await storage.putProfile(target);
        return response(200, 'SAVED');
    } catch {
        return response(503, 'SYNC_UNKNOWN');
    }
}
