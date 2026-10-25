// Ordinary application routes keep the approved upstream Functions router.
import { managedSync } from './sba-managed.js';

export async function onRequest(context) {
    const { request, env, next } = context;
    const pathname = new URL(request.url).pathname;
    if (env.SBA_PURPOSE === 'preview' && pathname !== '/.well-known/easyproxy-release.json') {
        const read = request.method === 'GET' || request.method === 'HEAD';
        const privateRead = request.method === 'GET' && ['/api/settings', '/api/manifest/aggregator-global'].includes(pathname);
        const controlled = request.method === 'POST' && ['/api/login', '/api/backup/export', '/api/sba/managed-sync'].includes(pathname);
        if (read && ['/', '/login', '/index.html'].includes(pathname)) {
            const url = new URL('/index.html', request.url);
            return env.ASSETS.fetch(new Request(url, request));
        }
        if (read && /^\/assets\/[A-Za-z0-9_./-]+$/.test(pathname) && !pathname.includes('..')) return env.ASSETS.fetch(request);
        if (!privateRead && !controlled) return new Response('Preview isolated', { status: 403, headers: { 'Cache-Control': 'no-store' } });
    }
    if (new URL(request.url).pathname === '/api/sba/managed-sync') return managedSync(context);
    if (new URL(request.url).pathname !== '/.well-known/easyproxy-release.json') return next();
    const headers = { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' };
    if (!['GET', 'HEAD'].includes(request.method)) {
        return new Response('Method Not Allowed', { status: 405, headers: { ...headers, Allow: 'GET, HEAD' } });
    }
    if (!/^[a-f0-9]{40}$/.test(env.SBA_RELEASE_SHA || '') ||
        !/^\d+\.\d+\.\d+$/.test(env.SBA_RELEASE_VERSION || '')) {
        return new Response('Release unavailable', { status: 503, headers });
    }
    const release = { schemaVersion: 1, applicationId: 'easyproxy-cloud',
        sourceSha: env.SBA_RELEASE_SHA, applicationVersion: env.SBA_RELEASE_VERSION };
    return new Response(request.method === 'HEAD' ? null : JSON.stringify(release), { headers });
}
