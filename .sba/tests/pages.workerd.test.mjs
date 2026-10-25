// Real generated Wrangler bundle + real MiSub frontend/D1 in local workerd.
// No Cloudflare credentials; outbound network is explicitly rejected.
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { createRuntime } from './miniflare-runtime.mjs';

const runtime = process.env.EASYPROXY_TEST_MINIFLARE;
const artifactRoot = process.env.EASYPROXY_TEST_ARTIFACT_ROOT;
const build = process.env.EASYPROXY_TEST_PAGES_BUILD;

for (const purpose of ['production', 'preview']) test(`MiSub actual Pages Functions bundle in real workerd (${purpose})`, {
    skip: !runtime || !artifactRoot || !build, timeout: 60000,
}, async t => {
    assert.ok([runtime, artifactRoot, build].every(value => path.isAbsolute(value)));
    await mkdir(artifactRoot, { recursive: true });
    const persistence = await mkdtemp(path.join(artifactRoot, 'pages-workerd-'));
    const bytes = await readFile(path.join(build, '_worker.bundle'));
    const boundary = bytes.subarray(2, bytes.indexOf('\r\n')).toString();
    const form = await new Response(bytes, { headers: { 'Content-Type': `multipart/form-data; boundary=${boundary}` } }).formData();
    const metadata = JSON.parse(form.get('metadata'));
    assert.equal(Object.hasOwn(metadata, 'bindings'), false);
    const script = await form.get(metadata.main_module).text();
    const outbound = [];
    const root = path.join(build, 'misub/dist');
    const assetRequests = [];
    const mf = await createRuntime(runtime, {
        modules: true, script, compatibilityDate: '2026-03-25', d1Databases: ['MISUB_DB'],
        cf: false,
        logRequests: false, host: '127.0.0.1', port: 0,
        bindings: { SBA_RELEASE_SHA: 'a'.repeat(40), SBA_RELEASE_VERSION: '1.1.0', SBA_PURPOSE: purpose,
            ADMIN_PASSWORD: 'fixture-admin-password', COOKIE_SECRET: 'fixture-cookie-secret', MANIFEST_TOKEN: 'fixture-manifest-token' },
        serviceBindings: { ASSETS: async request => {
            const pathname = new URL(request.url).pathname;
            assetRequests.push(pathname);
            const relative = pathname === '/' ? 'index.html' : pathname.slice(1);
            const target = path.resolve(root, relative);
            assert.ok(target.startsWith(root + path.sep));
            try {
                const body = await readFile(target);
                const type = target.endsWith('.html') ? 'text/html' : target.endsWith('.js') ? 'application/javascript' : 'application/octet-stream';
                return new Response(request.method === 'HEAD' ? null : body, { headers: { 'Content-Type': type } });
            } catch { return new Response('Not Found', { status: 404 }); }
        } },
        outboundService: request => { outbound.push(request.url); return new Response('Outbound disabled', { status: 502 }); },
    }, persistence);
    t.after(() => mf.dispose());
    await mf.ready;
    const database = await mf.getD1Database('MISUB_DB');
    const source = path.join(build, 'misub');
    for (const name of ['0001_initial.sql', '0002_cron_executions.sql']) {
        const sql = await readFile(path.join(source, 'migrations', name), 'utf8');
        for (const statement of sql.split(';').map(value => value.trim()).filter(Boolean)) await database.prepare(statement).run();
    }
    await database.prepare('INSERT INTO settings (key,value) VALUES (?,?)').bind('worker_settings_v1', JSON.stringify({
        mytoken: 'fixture-runtime-token', profileToken: 'fixture-profile-token',
        aggregatorSync: { enabled: false, runOnCron: false }, webdavBackup: { enabled: false },
    })).run();
    await database.prepare('INSERT INTO subscriptions (id,data) VALUES (?,?)').bind('fixture-source', JSON.stringify({
        id: 'fixture-source', name: 'Preserved fixture', url: 'https://fixture.invalid/subscription', enabled: false,
    })).run();

    await t.test('release GET/HEAD is read-only and method restricted', async () => {
        const before = await database.prepare('SELECT * FROM settings ORDER BY key').all();
        const response = await mf.dispatchFetch('https://misub.invalid/.well-known/easyproxy-release.json');
        assert.equal(response.status, 200);
        assert.equal(response.headers.get('Cache-Control'), 'no-store');
        assert.deepEqual(await response.json(), { schemaVersion: 1, applicationId: 'easyproxy-cloud', sourceSha: 'a'.repeat(40), applicationVersion: '1.1.0' });
        const head = await mf.dispatchFetch('https://misub.invalid/.well-known/easyproxy-release.json', { method: 'HEAD' });
        assert.equal(head.status, 200);
        assert.equal(await head.text(), '');
        const post = await mf.dispatchFetch('https://misub.invalid/.well-known/easyproxy-release.json', { method: 'POST' });
        assert.equal(post.status, 405);
        const after = await database.prepare('SELECT * FROM settings ORDER BY key').all();
        assert.deepEqual(after.results, before.results);
        assert.deepEqual(assetRequests, []);
    });
    await t.test('official Functions router still serves the real frontend', async () => {
        const response = await mf.dispatchFetch('https://misub.invalid/login');
        assert.equal(response.status, 200);
        const html = await response.text();
        assert.match(html, /<html/);
        assert.match(html, /assets\/js\//);
        assert.ok(!html.includes('fixture-cookie-secret'));
    });
    await t.test('original authentication, signed cookie and D1 data remain functional', async () => {
        const anonymous = await mf.dispatchFetch('https://misub.invalid/api/data');
        if (purpose === 'production') assert.equal((await anonymous.json()).authenticated, false);
        else assert.equal(anonymous.status, 403);
        const login = await mf.dispatchFetch('https://misub.invalid/api/login', { method: 'POST',
            headers: { Origin: 'https://misub.invalid', 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: 'fixture-admin-password' }) });
        assert.equal(login.status, 200);
        const cookie = login.headers.get('Set-Cookie');
        assert.match(cookie, /^auth_session=/);
        assert.match(cookie, /HttpOnly/);
        assert.match(cookie, /Secure/);
        const data = await mf.dispatchFetch('https://misub.invalid/api/data', { headers: { Cookie: cookie.split(';')[0] } });
        if (purpose === 'production') {
            assert.equal(data.status, 200);
            const value = await data.json();
            assert.ok(value.misubs.some(row => row.id === 'fixture-source' && row.name === 'Preserved fixture'));
            assert.ok(!JSON.stringify(value).includes('fixture-cookie-secret'));
        } else assert.equal(data.status, 403);
        assert.equal((await database.prepare('SELECT COUNT(*) AS count FROM subscriptions').first()).count, 1);
        const blocked = await mf.dispatchFetch('https://misub.invalid/api/login', { method: 'POST',
            headers: { Origin: 'https://unapproved.invalid', 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: 'fixture-admin-password' }) });
        assert.equal(blocked.status, 403);
    });
    await t.test('export and one managed diff save preserve unrelated rows without data/Cron requests', async () => {
        const login = await mf.dispatchFetch('https://misub.invalid/api/login', { method: 'POST',
            headers: { Origin: 'https://misub.invalid', 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: 'fixture-admin-password' }) });
        const headers = { Origin: 'https://misub.invalid', 'Content-Type': 'application/json',
            Cookie: login.headers.get('Set-Cookie').split(';')[0] };
        const originalSource = await database.prepare('SELECT * FROM subscriptions WHERE id = ?').bind('fixture-source').first();
        const originalSettings = await database.prepare('SELECT * FROM settings ORDER BY key').all();
        const exported = await mf.dispatchFetch('https://misub.invalid/api/backup/export', { method: 'POST', headers,
            body: JSON.stringify({ scope: 'dataAndSettings' }) });
        const before = await exported.json();
        assert.equal(before.success, true);
        assert.equal(before.exportData.source.storageType, 'd1');
        assert.deepEqual(Object.keys(before.exportData.data).sort(), ['profiles', 'ruleTemplates', 'settings', 'subscriptions']);
        const runtime = { id: 'proxy_runtime_node_1', kind: 'proxy_uri', name: 'Runtime fixture', enabled: true,
            input: 'trojan://fixture@192.0.2.1:443#fixture', url: 'trojan://fixture@192.0.2.1:443#fixture',
            probe_status: 'verified', detected_kind: 'proxy_uri', last_probe_at: 'sba-fixture',
            probe_message: 'Verified by shared EasyProxy runtime audit.', probe_input: 'trojan://fixture@192.0.2.1:443#fixture',
            options: { managed_by: 'easyproxy_runtime_sources', source_role: 'runtime_effective_proxy', sync_managed: true } };
        const connectorConfig = { local_protocol: 'http', access_token: 'fixture-ech-token' };
        const connector = { id: 'conn_ech_workers_pref_1', kind: 'connector', enabled: true, name: 'ECH fixture',
            input: 'https://fixture.account.workers.dev', url: 'https://fixture.account.workers.dev',
            connector_type: 'ech_worker', connector_config: connectorConfig,
            options: { connector_type: 'ech_worker', connector_config: connectorConfig } };
        const profile = { id: 'aggregator_global', customId: 'aggregator-global', name: 'Aggregator Global',
            enabled: true, isPublic: false, expiresAt: '', subscriptions: [], manualNodes: [runtime.id, connector.id],
            options: { managed_by: 'easyproxy_runtime_profile', sync_managed: true, source_role: 'runtime_effective_profile' } };
        const saved = await mf.dispatchFetch('https://misub.invalid/api/sba/managed-sync', { method: 'POST', headers,
            body: JSON.stringify({ operationId: 'b'.repeat(32), sourceSha: 'a'.repeat(40), before: {
                subscriptions: before.exportData.data.subscriptions, profiles: before.exportData.data.profiles },
                diff: { subscriptions: { added: [runtime, connector], updated: [], removed: [] },
                profiles: { added: [profile], updated: [], removed: [] } } }) });
        assert.equal(saved.status, 200);
        assert.equal((await saved.json()).success, true);
        const after = await mf.dispatchFetch('https://misub.invalid/api/backup/export', { method: 'POST', headers,
            body: JSON.stringify({ scope: 'dataAndSettings' }) });
        const snapshot = (await after.json()).exportData.data;
        assert.equal(snapshot.subscriptions.length, 3);
        assert.deepEqual(await database.prepare('SELECT * FROM subscriptions WHERE id = ?').bind('fixture-source').first(), originalSource);
        assert.deepEqual((await database.prepare('SELECT * FROM settings ORDER BY key').all()).results, originalSettings.results);
        const response = await mf.dispatchFetch('https://misub.invalid/api/manifest/aggregator-global', {
            headers: { Authorization: 'Bearer fixture-manifest-token' } });
        const manifest = await response.json();
        assert.equal(manifest.success, true);
        assert.equal(manifest.profile.customId, profile.customId);
        assert.deepEqual(manifest.sources.map(row => row.id), profile.manualNodes);
        assert.equal(manifest.sources[0].input, runtime.input);
        assert.deepEqual(manifest.sources[1].options.connector_config, connectorConfig);
        const refreshPayload = { operationId: 'c'.repeat(32), sourceSha: 'a'.repeat(40), before: {
            subscriptions: snapshot.subscriptions, profiles: snapshot.profiles }, diff: {
            subscriptions: { added: [], updated: [{ ...runtime, last_probe_at: 'sba-next' }], removed: [] },
            profiles: { added: [], updated: [profile], removed: [] } } };
        const refresh = await mf.dispatchFetch('https://misub.invalid/api/sba/managed-sync', { method: 'POST', headers,
            body: JSON.stringify(refreshPayload) });
        assert.equal(refresh.status, 200);
        assert.equal((await refresh.json()).success, true);
        assert.deepEqual(await database.prepare('SELECT * FROM subscriptions WHERE id = ?').bind('fixture-source').first(), originalSource);
        assert.deepEqual((await database.prepare('SELECT * FROM settings ORDER BY key').all()).results, originalSettings.results);
    });
    await t.test('managed endpoint rejects unauthenticated, cross-origin, wrong-source and non-managed writes', async () => {
        const login = await mf.dispatchFetch('https://misub.invalid/api/login', { method: 'POST',
            headers: { Origin: 'https://misub.invalid', 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: 'fixture-admin-password' }) });
        const headers = { Origin: 'https://misub.invalid', 'Content-Type': 'application/json',
            Cookie: login.headers.get('Set-Cookie').split(';')[0] };
        const tables = ['subscriptions', 'profiles', 'settings'];
        const snapshot = async () => Promise.all(tables.map(table => database.prepare(`SELECT * FROM ${table} ORDER BY 1`).all().then(value => value.results)));
        const before = await snapshot();
        const body = { operationId: 'd'.repeat(32), sourceSha: 'a'.repeat(40), before: { subscriptions: [], profiles: [] },
            diff: { subscriptions: { added: [], updated: [], removed: [] },
                profiles: { added: [], updated: [], removed: [] } } };
        const endpoint = 'https://misub.invalid/api/sba/managed-sync';
        const anonymous = await mf.dispatchFetch(endpoint, { method: 'POST', headers: { ...headers, Cookie: '' }, body: JSON.stringify(body) });
        assert.equal(anonymous.status, 401);
        const crossOrigin = await mf.dispatchFetch(endpoint, { method: 'POST', headers: { ...headers, Origin: 'https://other.invalid' }, body: JSON.stringify(body) });
        assert.equal(crossOrigin.status, 403);
        const wrongSource = await mf.dispatchFetch(endpoint, { method: 'POST', headers, body: JSON.stringify({ ...body, sourceSha: 'f'.repeat(40) }) });
        assert.equal(wrongSource.status, 400);
        for (const change of [
            { ...body.diff, subscriptions: { added: [{ id: 'user-source', kind: 'proxy_uri', input: 'trojan://fixture', url: 'trojan://fixture', enabled: true }], updated: [], removed: [] } },
            { ...body.diff, subscriptions: { added: [], updated: [], removed: ['fixture-source'] } },
            { ...body.diff, profiles: { added: [{ id: 'user-profile', customId: 'user-profile', enabled: true, manualNodes: [], subscriptions: [] }], updated: [], removed: [] } },
        ]) {
            const rejected = await mf.dispatchFetch(endpoint, { method: 'POST', headers, body: JSON.stringify({ ...body, diff: change }) });
            assert.ok([400, 503].includes(rejected.status));
        }
        const method = await mf.dispatchFetch(endpoint);
        assert.equal(method.status, purpose === 'production' ? 405 : 403);
        assert.deepEqual(await snapshot(), before);
    });
    if (purpose === 'preview') await t.test('preview denies side-effect routes even with valid administrator cookie', async () => {
        const login = await mf.dispatchFetch('https://misub.invalid/api/login', { method: 'POST',
            headers: { Origin: 'https://misub.invalid', 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: 'fixture-admin-password' }) });
        const headers = { Cookie: login.headers.get('Set-Cookie').split(';')[0], Origin: 'https://misub.invalid',
            'Content-Type': 'application/json' };
        const snapshot = async () => Promise.all(['subscriptions', 'profiles', 'settings', 'cron_executions'].map(table =>
            database.prepare(`SELECT * FROM ${table} ORDER BY 1`).all().then(value => value.results)));
        const before = await snapshot();
        for (const [method, pathname] of [['GET', '/cron'], ['POST', '/api/cron'], ['GET', '/api/data'],
            ['POST', '/api/backup/restore'], ['POST', '/api/settings'], ['POST', '/api/misubs'],
            ['GET', '/sub/aggregator-global'], ['GET', '/api/aggregator/sync']]) {
            const response = await mf.dispatchFetch('https://misub.invalid' + pathname, { method, headers,
                ...(method === 'POST' ? { body: '{}' } : {}) });
            assert.equal(response.status, 403, pathname);
            assert.equal(response.headers.get('Cache-Control'), 'no-store');
            assert.equal(await response.text(), 'Preview isolated');
        }
        assert.deepEqual(await snapshot(), before);
    });
    assert.deepEqual(outbound, []);
});
