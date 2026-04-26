import { afterEach, describe, expect, it, vi } from 'vitest';
import { StorageFactory } from '../../functions/storage-adapter.js';
import { handleManifestRequest } from '../../functions/modules/source-handler.js';
import { KV_KEY_PROFILES, KV_KEY_SUBS } from '../../functions/modules/config.js';

function createStorageAdapter({ sources = [], profiles = [] } = {}) {
    return {
        get: vi.fn((key) => {
            if (key === KV_KEY_SUBS) {
                return Promise.resolve(sources);
            }

            if (key === KV_KEY_PROFILES) {
                return Promise.resolve(profiles);
            }

            return Promise.resolve(null);
        })
    };
}

function createAuthorizedRequest(url, token = 'manifest-secret') {
    return new Request(url, {
        headers: {
            Authorization: `Bearer ${token}`
        }
    });
}

describe('handleManifestRequest', () => {
    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('returns 503 when MANIFEST_TOKEN is not configured', async () => {
        const response = await handleManifestRequest(
            new Request('https://misub.example.com/api/manifest/default'),
            {},
            'default'
        );

        expect(response.status).toBe(503);
        await expect(response.json()).resolves.toMatchObject({
            success: false,
            error: 'MANIFEST_TOKEN is not configured'
        });
    });

    it('returns 401 when bearer token is missing or invalid', async () => {
        const response = await handleManifestRequest(
            new Request('https://misub.example.com/api/manifest/default'),
            { MANIFEST_TOKEN: 'manifest-secret' },
            'default'
        );

        expect(response.status).toBe(401);
        await expect(response.json()).resolves.toMatchObject({
            success: false,
            error: 'Unauthorized'
        });
    });

    it('returns 404 for missing or disabled profiles', async () => {
        vi.spyOn(StorageFactory, 'getStorageType').mockResolvedValue('d1');
        vi.spyOn(StorageFactory, 'createAdapter').mockReturnValue(createStorageAdapter({
            sources: [],
            profiles: [
                {
                    id: 'disabled-profile',
                    customId: 'disabled',
                    enabled: false,
                    subscriptions: [],
                    manualNodes: []
                }
            ]
        }));

        const response = await handleManifestRequest(
            createAuthorizedRequest('https://misub.example.com/api/manifest/disabled'),
            { MANIFEST_TOKEN: 'manifest-secret' },
            'disabled'
        );

        expect(response.status).toBe(404);
        await expect(response.json()).resolves.toMatchObject({
            success: false,
            error: 'Profile not found or disabled'
        });
    });

    it('resolves profiles by customId and includes selected connector sources in manifest output', async () => {
        vi.spyOn(StorageFactory, 'getStorageType').mockResolvedValue('d1');
        vi.spyOn(StorageFactory, 'createAdapter').mockReturnValue(createStorageAdapter({
            sources: [
                {
                    id: 'sub-1',
                    kind: 'subscription',
                    name: 'Shared Subscription',
                    enabled: true,
                    input: 'https://example.com/sub'
                },
                {
                    id: 'sub-disabled',
                    kind: 'subscription',
                    name: 'Disabled Subscription',
                    enabled: false,
                    input: 'https://disabled.example.com/sub'
                },
                {
                    id: 'proxy-1',
                    kind: 'proxy_uri',
                    name: 'Residential Proxy',
                    enabled: true,
                    input: 'user:pass@proxy.example.com:8080',
                    group: 'residential'
                },
                {
                    id: 'proxy-dup',
                    kind: 'proxy_uri',
                    name: 'Residential Proxy Duplicate',
                    enabled: true,
                    input: 'http://user:pass@proxy.example.com:8080',
                    group: 'duplicate'
                },
                {
                    id: 'connector-1',
                    kind: 'connector',
                    name: 'ECH Connector',
                    enabled: true,
                    input: 'https://ech.example.com/connect',
                    options: {
                        connector_type: 'ech_worker',
                        connector_config: {
                            local_protocol: 'socks5'
                        }
                    }
                }
            ],
            profiles: [
                {
                    id: 'profile-1',
                    customId: 'team-default',
                    name: 'Default Team Profile',
                    description: 'Machine profile',
                    enabled: true,
                    subscriptions: ['sub-1', 'sub-disabled'],
                    manualNodes: ['proxy-1', 'proxy-dup', 'connector-1']
                }
            ]
        }));

        const response = await handleManifestRequest(
            createAuthorizedRequest('https://misub.example.com/api/manifest/team-default'),
            { MANIFEST_TOKEN: 'manifest-secret' },
            'team-default'
        );

        expect(response.status).toBe(200);

        const payload = await response.json();
        expect(payload.success).toBe(true);
        expect(payload.profile).toMatchObject({
            id: 'profile-1',
            customId: 'team-default',
            name: 'Default Team Profile'
        });
        expect(payload.sources).toEqual([
            expect.objectContaining({
                id: 'sub-1',
                kind: 'subscription',
                input: 'https://example.com/sub'
            }),
            expect.objectContaining({
                id: 'proxy-1',
                kind: 'proxy_uri',
                input: 'http://user:pass@proxy.example.com:8080',
                group: 'residential'
            }),
            expect.objectContaining({
                id: 'connector-1',
                kind: 'connector',
                input: 'https://ech.example.com/connect',
                options: expect.objectContaining({
                    connector_type: 'ech_worker'
                })
            })
        ]);
    });

    it('resolves profiles by internal id as well as customId', async () => {
        vi.spyOn(StorageFactory, 'getStorageType').mockResolvedValue('d1');
        vi.spyOn(StorageFactory, 'createAdapter').mockReturnValue(createStorageAdapter({
            sources: [
                {
                    id: 'sub-1',
                    kind: 'subscription',
                    name: 'Shared Subscription',
                    enabled: true,
                    input: 'https://example.com/sub'
                }
            ],
            profiles: [
                {
                    id: 'profile-internal',
                    customId: 'team-default',
                    name: 'Internal Profile',
                    enabled: true,
                    subscriptions: ['sub-1'],
                    manualNodes: []
                }
            ]
        }));

        const response = await handleManifestRequest(
            createAuthorizedRequest('https://misub.example.com/api/manifest/profile-internal'),
            { MANIFEST_TOKEN: 'manifest-secret' },
            'profile-internal'
        );

        expect(response.status).toBe(200);
        const payload = await response.json();
        expect(payload.profile.id).toBe('profile-internal');
        expect(payload.sources).toHaveLength(1);
    });
});
