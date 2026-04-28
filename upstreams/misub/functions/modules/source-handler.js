import { StorageFactory } from '../storage-adapter.js';
import { KV_KEY_PROFILES, KV_KEY_SUBS } from './config.js';
import { createJsonResponse, getManifestToken } from './utils.js';
import {
    SOURCE_KIND_CONNECTOR,
    SOURCE_KIND_PROXY_URI,
    SOURCE_KIND_SUBSCRIPTION,
    SOURCE_PROBE_STATUS_SKIPPED,
    SOURCE_PROBE_STATUS_VERIFIED,
    dedupeSources,
    isLikelyHTTPProxyInput,
    normalizeSourceCollection,
    normalizeSourceItem,
    toManifestSource
} from '../../src/shared/source-utils.js';
import { probeSourceItem, probeSourceItems } from './source-probe.js';

function extractBearerToken(request) {
    const authHeader = request.headers.get('Authorization') || '';
    const match = authHeader.match(/^Bearer\s+(.+)$/i);
    return match ? match[1].trim() : '';
}

function isManifestAuthorized(request, env) {
    const expectedToken = getManifestToken(env);
    if (!expectedToken) {
        return { ok: false, status: 503, message: 'MANIFEST_TOKEN is not configured' };
    }

    const providedToken = extractBearerToken(request);
    if (!providedToken || providedToken !== expectedToken) {
        return { ok: false, status: 401, message: 'Unauthorized' };
    }

    return { ok: true };
}

function selectProfileSources(profile, allSources) {
    const sourceMap = new Map(allSources.map(source => [source.id, source]));
    const selected = [];

    for (const id of profile.subscriptions || []) {
        const source = sourceMap.get(id);
        if (!source || source.enabled !== true || !isManifestEffectiveSource(source)) continue;
        if (source.kind !== SOURCE_KIND_SUBSCRIPTION) continue;
        selected.push(source);
    }

    for (const id of profile.manualNodes || []) {
        const source = sourceMap.get(id);
        if (!source || source.enabled !== true || !isManifestEffectiveSource(source)) continue;
        if (![SOURCE_KIND_PROXY_URI, SOURCE_KIND_CONNECTOR].includes(source.kind)) continue;
        selected.push(source);
    }

    return dedupeSources(selected);
}

function isManifestEffectiveSource(source) {
    const normalized = normalizeSourceItem(source);
    if (!normalized || normalized.enabled !== true) {
        return false;
    }
    if (normalized.kind === SOURCE_KIND_CONNECTOR) {
        return true;
    }
    if (normalized.probe_status === SOURCE_PROBE_STATUS_VERIFIED) {
        return true;
    }
    if (normalized.kind === SOURCE_KIND_PROXY_URI && normalized.probe_status === SOURCE_PROBE_STATUS_SKIPPED) {
        return isLikelyHTTPProxyInput(normalized.input);
    }
    return false;
}

export async function handleManifestRequest(request, env, profileIdentifier) {
    if (request.method !== 'GET') {
        return createJsonResponse({ success: false, error: 'Method Not Allowed' }, 405);
    }

    const authResult = isManifestAuthorized(request, env);
    if (!authResult.ok) {
        return createJsonResponse({ success: false, error: authResult.message }, authResult.status);
    }

    const profileId = String(profileIdentifier || '').trim();
    if (!profileId) {
        return createJsonResponse({ success: false, error: 'Missing profile id' }, 400);
    }

    const storageAdapter = StorageFactory.createAdapter(env, await StorageFactory.getStorageType(env));
    const [rawSources, rawProfiles] = await Promise.all([
        storageAdapter.get(KV_KEY_SUBS).then(result => result || []),
        storageAdapter.get(KV_KEY_PROFILES).then(result => result || [])
    ]);

    const profile = rawProfiles.find(item =>
        item && item.enabled &&
        ((item.customId && item.customId === profileId) || item.id === profileId)
    );

    if (!profile) {
        return createJsonResponse({ success: false, error: 'Profile not found or disabled' }, 404);
    }

    const normalizedSources = normalizeSourceCollection(rawSources);
    const selectedSources = selectProfileSources(profile, normalizedSources).map(toManifestSource);

    return createJsonResponse({
        success: true,
        version: 'v1',
        generated_at: new Date().toISOString(),
        profile: {
            id: profile.id,
            customId: profile.customId || '',
            name: profile.name || '',
            description: profile.description || ''
        },
        sources: selectedSources
    });
}

export async function handleSourceProbeRequest(request) {
    if (request.method !== 'POST') {
        return createJsonResponse({ success: false, error: 'Method Not Allowed' }, 405);
    }

    let requestData;
    try {
        requestData = await request.json();
    } catch (_) {
        return createJsonResponse({ success: false, error: 'Invalid JSON payload' }, 400);
    }

    const source = normalizeSourceItem(requestData?.source);
    if (!source.input) {
        return createJsonResponse({ success: false, error: 'Missing source input' }, 400);
    }

    const probedSource = await probeSourceItem(source);
    return createJsonResponse({
        success: true,
        data: {
            source: probedSource
        }
    });
}

export async function handleBatchSourceProbeRequest(request) {
    if (request.method !== 'POST') {
        return createJsonResponse({ success: false, error: 'Method Not Allowed' }, 405);
    }

    let requestData;
    try {
        requestData = await request.json();
    } catch (_) {
        return createJsonResponse({ success: false, error: 'Invalid JSON payload' }, 400);
    }

    const sources = Array.isArray(requestData?.sources)
        ? requestData.sources.map(item => normalizeSourceItem(item)).filter(item => item.input)
        : [];

    if (sources.length === 0) {
        return createJsonResponse({ success: false, error: 'Missing sources input' }, 400);
    }

    if (sources.length > 50) {
        return createJsonResponse({ success: false, error: 'Batch size too large (max 50)' }, 400);
    }

    const probedSources = await probeSourceItems(sources);
    return createJsonResponse({
        success: true,
        data: {
            sources: probedSources
        }
    });
}
