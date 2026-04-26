import { describe, expect, it } from 'vitest';
import { handleLogin } from '../../functions/modules/auth-middleware.js';

describe('handleLogin', () => {
    it('fails closed when ADMIN_PASSWORD is not configured', async () => {
        const request = new Request('https://misub.example.com/api/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: 'anything' })
        });

        const response = await handleLogin(request, {});
        expect(response.status).toBe(503);
    });
});
