import { StorageFactory } from '../storage-adapter.js';
import { createJsonResponse } from './utils.js';
import { runAggregatorSync } from './aggregator-sync.js';

export async function handleAggregatorSyncRequest(request, env) {
    if (request.method !== 'POST') {
        return createJsonResponse({ success: false, error: 'Method Not Allowed' }, 405);
    }

    const storageAdapter = StorageFactory.createAdapter(env, await StorageFactory.getStorageType(env));

    try {
        let settingsOverride = null;
        try {
            const payload = await request.json();
            if (payload?.settings && typeof payload.settings === 'object') {
                settingsOverride = payload.settings;
            }
        } catch (_) {
            settingsOverride = null;
        }

        const result = await runAggregatorSync(env, storageAdapter, settingsOverride);
        return createJsonResponse({
            success: true,
            data: {
                summary: result.summary,
                sourceCount: Array.isArray(result.sources) ? result.sources.length : 0,
                aggregatorSync: result.nextSettings?.aggregatorSync || null
            }
        });
    } catch (error) {
        return createJsonResponse({
            success: false,
            error: error?.message || 'Aggregator sync failed'
        }, 502);
    }
}
