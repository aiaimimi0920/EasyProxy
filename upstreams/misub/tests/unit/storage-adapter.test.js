import { afterEach, describe, expect, it, vi } from 'vitest';
import { SettingsCache, StorageFactory, STORAGE_TYPES } from '../../functions/storage-adapter.js';

function createKvNamespace() {
    return {
        get: vi.fn(),
        put: vi.fn(),
        delete: vi.fn(),
        list: vi.fn()
    };
}

describe('StorageFactory', () => {
    afterEach(() => {
        vi.restoreAllMocks();
        SettingsCache.clear();
    });

    it('defaults to D1 when MISUB_DB is available and no explicit storageType is saved', async () => {
        vi.spyOn(SettingsCache, 'get').mockResolvedValue(null);

        const storageType = await StorageFactory.getStorageType({ MISUB_DB: {} });

        expect(storageType).toBe(STORAGE_TYPES.D1);
    });

    it('defaults to KV when D1 is unavailable but KV is bound', async () => {
        vi.spyOn(SettingsCache, 'get').mockResolvedValue(null);

        const storageType = await StorageFactory.getStorageType({ MISUB_KV: createKvNamespace() });

        expect(storageType).toBe(STORAGE_TYPES.KV);
    });

    it('honors an explicitly saved storageType', async () => {
        vi.spyOn(SettingsCache, 'get').mockResolvedValue({ storageType: STORAGE_TYPES.KV });

        const storageType = await StorageFactory.getStorageType({ MISUB_DB: {}, MISUB_KV: createKvNamespace() });

        expect(storageType).toBe(STORAGE_TYPES.KV);
    });

    it('falls back to D1 adapter when KV is requested but only MISUB_DB is available', () => {
        const adapter = StorageFactory.createAdapter({ MISUB_DB: {} }, STORAGE_TYPES.KV);

        expect(adapter.constructor.name).toBe('D1StorageAdapter');
    });

    it('fails closed when no storage bindings are available', async () => {
        const adapter = StorageFactory.createAdapter({}, STORAGE_TYPES.D1);

        await expect(adapter.put('worker_settings_v1', { mytoken: 'auto' })).rejects.toThrow('storage is unavailable');
        await expect(adapter.delete('worker_settings_v1')).rejects.toThrow('storage is unavailable');
    });
});
