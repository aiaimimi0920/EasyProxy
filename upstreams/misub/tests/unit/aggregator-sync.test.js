import { describe, expect, it, vi } from 'vitest';
import { syncAggregatorArtifacts } from '../../functions/modules/aggregator-sync.js';

function createJsonResponse(data) {
    return {
        ok: true,
        status: 200,
        async json() {
            return data;
        }
    };
}

function createTextResponse(text, { status = 200, contentType = 'text/plain; charset=utf-8' } = {}) {
    return {
        ok: status >= 200 && status < 300,
        status,
        headers: {
            get(name) {
                if (String(name).toLowerCase() === 'content-type') return contentType;
                return null;
            }
        },
        async text() {
            return text;
        }
    };
}

describe('syncAggregatorArtifacts', () => {
    it('keeps crawled subscriptions as internal discovery sources and maintains a stable public source/profile', async () => {
        const fetchMock = vi.fn(async (input) => {
            const url = String(input);
            if (url === 'https://sub.aiaimimi.com/internal/crawledsubs.json') {
                return createJsonResponse({
                    'https://fresh.example.com/sub': {
                        origin: 'GITHUB',
                        push_to: ['public'],
                        discovered: true
                    },
                    'https://duplicate.example.com/sub': {
                        origin: 'TELEGRAM',
                        push_to: ['public'],
                        discovered: true
                    }
                });
            }

            if (url === 'https://fresh.example.com/sub') {
                return createTextResponse('proxies:\n  - { name: fresh, type: ss, server: fresh.example.com, port: 443, cipher: aes-128-gcm, password: test }', {
                    contentType: 'text/yaml; charset=utf-8'
                });
            }

            if (url === 'https://sub.aiaimimi.com/subs/clash.yaml') {
                return createTextResponse('proxies:\n  - { name: stable, type: ss, server: stable.example.com, port: 443, cipher: aes-128-gcm, password: test }', {
                    contentType: 'text/yaml; charset=utf-8'
                });
            }

            throw new Error(`Unexpected fetch URL in test: ${url}`);
        });

        const result = await syncAggregatorArtifacts({
            sources: [
                {
                    id: 'user-sub',
                    kind: 'subscription',
                    name: 'User Managed Duplicate',
                    enabled: true,
                    input: 'https://duplicate.example.com/sub'
                },
                {
                    id: 'managed-missing',
                    kind: 'subscription',
                    name: 'Old Managed Source',
                    enabled: true,
                    input: 'https://stale.example.com/sub',
                    options: {
                        managed_by: 'aggregator_sync',
                        sync_source: 'aggregator_crawledsubs'
                    }
                },
                {
                    id: 'manual-stable',
                    kind: 'subscription',
                    name: 'Manual Stable Copy',
                    enabled: true,
                    input: 'https://sub.aiaimimi.com/subs/clash.yaml',
                    group: 'HealthCheck'
                }
            ],
            profiles: [],
            settings: {
                aggregatorSync: {
                    enabled: true,
                    sourceUrl: 'https://sub.aiaimimi.com/internal/crawledsubs.json',
                    managedGroup: 'Aggregator Discovery',
                    namePrefix: 'Aggregator Discovery',
                    secondaryProbeEnabled: true,
                    stableSourceEnabled: true,
                    stableSourceUrl: 'https://sub.aiaimimi.com/subs/clash.yaml',
                    stableSourceName: 'Aggregator Stable',
                    stableSourceGroup: 'Aggregator Stable',
                    defaultPublicProfileEnabled: true,
                    defaultPublicProfileId: 'aggregator_global',
                    defaultPublicProfileCustomId: 'aggregator-global',
                    defaultPublicProfileName: 'Aggregator Global',
                    defaultPublicProfileDescription: 'Public stable aggregator output managed by MiSub',
                    autoDisableMissing: true
                }
            },
            fetchImpl: fetchMock
        });

        expect(fetchMock).toHaveBeenCalledTimes(3);
        expect(result.summary).toMatchObject({
            status: 'success',
            totalRemote: 2,
            discoveryCreated: 1,
            preservedDuplicates: 1,
            discoveryDisabledMissing: 1,
            stableCreated: 1,
            publicProfileUpdated: 1,
            discoveryProbedCount: 1,
            discoveryVerifiedCount: 1
        });

        const discoverySource = result.sources.find(item => item.input === 'https://fresh.example.com/sub');
        expect(discoverySource).toMatchObject({
            kind: 'subscription',
            enabled: true,
            group: 'Aggregator Discovery'
        });
        expect(discoverySource.options).toMatchObject({
            managed_by: 'aggregator_sync',
            source_role: 'internal_discovery',
            visibility: 'internal'
        });
        expect(discoverySource.probe_status).toBe('verified');
        expect(discoverySource.detected_kind).toBe('subscription');

        const duplicateSource = result.sources.find(item => item.id === 'user-sub');
        expect(duplicateSource.options?.managed_by).toBeUndefined();

        const staleManagedSource = result.sources.find(item => item.id === 'managed-missing');
        expect(staleManagedSource.enabled).toBe(false);
        expect(staleManagedSource.options.aggregator_missing).toBe(true);

        const stableSource = result.sources.find(item => item.input === 'https://sub.aiaimimi.com/subs/clash.yaml');
        expect(stableSource).toMatchObject({
            kind: 'subscription',
            enabled: true,
            name: 'Aggregator Stable',
            group: 'Aggregator Stable'
        });
        expect(stableSource.options).toMatchObject({
            managed_by: 'aggregator_stable',
            source_role: 'stable_output',
            visibility: 'public_default'
        });
        expect(stableSource.id).toBe('sub_aggregator_stable');
        expect(stableSource.probe_status).toBe('verified');

        expect(result.profiles).toContainEqual(expect.objectContaining({
            id: 'aggregator_global',
            customId: 'aggregator-global',
            name: 'Aggregator Global',
            isPublic: true,
            subscriptions: [stableSource.id]
        }));

        expect(result.nextSettings.aggregatorSync).toMatchObject({
            lastSyncStatus: 'success',
            lastImportedCount: 3,
            lastDiscoveryImportedCount: 2,
            lastDiscoveryProbedCount: 1,
            lastDiscoveryVerifiedCount: 1,
            lastDiscoveryUnreachableCount: 0,
            lastDiscoveryInconclusiveCount: 0,
            lastDiscoverySkippedCount: 0
        });
    });

    it('adds explicitly configured connector ids into the managed public profile', async () => {
        const fetchMock = vi.fn(async (input) => {
            const url = String(input);
            if (url === 'https://sub.aiaimimi.com/internal/crawledsubs.json') {
                return createJsonResponse({});
            }
            if (url === 'https://sub.aiaimimi.com/subs/clash.yaml') {
                return createTextResponse('proxies:\n  - { name: stable, type: ss, server: stable.example.com, port: 443, cipher: aes-128-gcm, password: test }', {
                    contentType: 'text/yaml; charset=utf-8'
                });
            }

            throw new Error(`Unexpected fetch URL in test: ${url}`);
        });

        const result = await syncAggregatorArtifacts({
            sources: [
                {
                    id: 'conn_ech_workers_pref_1',
                    kind: 'connector',
                    name: 'ECH Worker Preferred 1',
                    enabled: true,
                    input: 'https://proxyservice-ech-workers.vmjcv666.workers.dev:443',
                    connector_type: 'ech_worker',
                    connector_config: {
                        access_token: 'token-1'
                    }
                },
                {
                    id: 'conn_ech_workers_pref_2',
                    kind: 'connector',
                    name: 'ECH Worker Preferred 2',
                    enabled: true,
                    input: 'https://proxyservice-ech-workers.vmjcv666.workers.dev:443',
                    connector_type: 'ech_worker',
                    connector_config: {
                        access_token: 'token-2',
                        server_ip: '198.41.132.114'
                    }
                }
            ],
            profiles: [],
            settings: {
                aggregatorSync: {
                    enabled: true,
                    stableSourceEnabled: true,
                    defaultPublicProfileEnabled: true,
                    defaultPublicProfileConnectorIds: [
                        'conn_ech_workers_pref_1',
                        'conn_ech_workers_pref_2',
                        'conn_ech_workers_pref_1'
                    ]
                }
            },
            fetchImpl: fetchMock
        });

        const managedProfile = result.profiles.find(item => item.id === 'aggregator_global');
        expect(managedProfile).toBeTruthy();
        expect(managedProfile.subscriptions).toEqual(['sub_aggregator_stable']);
        expect(managedProfile.manualNodes).toEqual([
            'conn_ech_workers_pref_1',
            'conn_ech_workers_pref_2'
        ]);
    });

    it('preserves existing connector selections when no explicit connector ids are configured', async () => {
        const fetchMock = vi.fn(async (input) => {
            const url = String(input);
            if (url === 'https://sub.aiaimimi.com/internal/crawledsubs.json') {
                return createJsonResponse({});
            }
            if (url === 'https://sub.aiaimimi.com/subs/clash.yaml') {
                return createTextResponse('proxies:\n  - { name: stable, type: ss, server: stable.example.com, port: 443, cipher: aes-128-gcm, password: test }', {
                    contentType: 'text/yaml; charset=utf-8'
                });
            }

            throw new Error(`Unexpected fetch URL in test: ${url}`);
        });

        const result = await syncAggregatorArtifacts({
            sources: [
                {
                    id: 'conn_ech_workers_pref_3',
                    kind: 'connector',
                    name: 'ECH Worker Preferred 3',
                    enabled: true,
                    input: 'https://proxyservice-ech-workers.vmjcv666.workers.dev:443',
                    connector_type: 'ech_worker',
                    connector_config: {
                        access_token: 'token-3'
                    }
                },
                {
                    id: 'user-direct-node',
                    kind: 'proxy_uri',
                    name: 'Direct Residential',
                    enabled: true,
                    input: 'http://user:pass@example.com:8080'
                }
            ],
            profiles: [
                {
                    id: 'aggregator_global',
                    customId: 'aggregator-global',
                    name: 'Aggregator Global',
                    enabled: true,
                    subscriptions: ['sub_aggregator_stable'],
                    manualNodes: ['conn_ech_workers_pref_3', 'user-direct-node'],
                    isPublic: true
                }
            ],
            settings: {
                aggregatorSync: {
                    enabled: true,
                    stableSourceEnabled: true,
                    defaultPublicProfileEnabled: true
                }
            },
            fetchImpl: fetchMock
        });

        const managedProfile = result.profiles.find(item => item.id === 'aggregator_global');
        expect(managedProfile.manualNodes).toEqual(['conn_ech_workers_pref_3']);
    });
});
