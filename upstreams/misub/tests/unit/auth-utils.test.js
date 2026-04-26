import { describe, expect, it, vi } from 'vitest';
import { ensureStableSettingsTokens, getAdminPassword, getCookieSecret } from '../../functions/modules/utils.js';

describe('auth token helpers', () => {
    it('keeps cookie secret stable within the same runtime when KV/env are absent', async () => {
        const first = await getCookieSecret({});
        const second = await getCookieSecret({});

        expect(first).toBe(second);
        expect(first).not.toHaveLength(0);
    });

    it('fails closed when no admin password is configured', async () => {
        await expect(getAdminPassword({})).resolves.toBe('');
    });

    it('generates and persists stable share tokens when placeholders are present', async () => {
        const put = vi.fn().mockResolvedValue(true);
        const storageAdapter = { put };

        const result = await ensureStableSettingsTokens(storageAdapter, {
            mytoken: 'auto',
            profileToken: 'profiles'
        });

        expect(result.mytoken).not.toBe('auto');
        expect(result.profileToken).not.toBe('profiles');
        expect(result.mytoken).toHaveLength(32);
        expect(result.profileToken).toHaveLength(32);
        expect(put).toHaveBeenCalledTimes(1);
    });
});
