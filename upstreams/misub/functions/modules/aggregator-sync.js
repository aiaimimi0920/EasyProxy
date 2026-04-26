import { SettingsCache } from '../storage-adapter.js';
import { KV_KEY_SETTINGS, KV_KEY_SUBS, KV_KEY_PROFILES, DEFAULT_SETTINGS } from './config.js';
import {
    normalizeSourceCollection,
    getSourceKey,
    isConnectorSource
} from '../../src/shared/source-utils.js';
import { probeSourceItems } from './source-probe.js';

const AGGREGATOR_DISCOVERY_MANAGED_BY = 'aggregator_sync';
const AGGREGATOR_STABLE_MANAGED_BY = 'aggregator_stable';
const AGGREGATOR_PUBLIC_PROFILE_MANAGED_BY = 'aggregator_public_profile';

function normalizeString(value) {
    return typeof value === 'string' ? value.trim() : '';
}

function normalizeStringArray(value) {
    if (!Array.isArray(value)) {
        return [];
    }

    const seen = new Set();
    const normalized = [];
    for (const entry of value) {
        const id = normalizeString(entry);
        if (!id || seen.has(id)) {
            continue;
        }
        seen.add(id);
        normalized.push(id);
    }
    return normalized;
}

function cloneObject(value, fallback = {}) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
        return { ...fallback };
    }
    return JSON.parse(JSON.stringify(value));
}

function generateManagedId(prefix = 'sub') {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
        return `${prefix}_${crypto.randomUUID()}`;
    }
    return `${prefix}_${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
}

function getDefaultAggregatorSyncSettings() {
    return cloneObject(DEFAULT_SETTINGS.aggregatorSync);
}

function resolveAggregatorSyncSettings(settings = {}) {
    const resolved = {
        ...getDefaultAggregatorSyncSettings(),
        ...cloneObject(settings?.aggregatorSync)
    };

    resolved.sourceUrl = normalizeString(resolved.sourceUrl) || DEFAULT_SETTINGS.aggregatorSync.sourceUrl;
    resolved.managedGroup = normalizeString(resolved.managedGroup) || DEFAULT_SETTINGS.aggregatorSync.managedGroup;
    resolved.namePrefix = normalizeString(resolved.namePrefix) || DEFAULT_SETTINGS.aggregatorSync.namePrefix;
    resolved.secondaryProbeEnabled = resolved.secondaryProbeEnabled !== false;
    resolved.stableSourceEnabled = resolved.stableSourceEnabled !== false;
    resolved.stableSourceUrl = normalizeString(resolved.stableSourceUrl) || DEFAULT_SETTINGS.aggregatorSync.stableSourceUrl;
    resolved.stableSourceName = normalizeString(resolved.stableSourceName) || DEFAULT_SETTINGS.aggregatorSync.stableSourceName;
    resolved.stableSourceGroup = normalizeString(resolved.stableSourceGroup) || DEFAULT_SETTINGS.aggregatorSync.stableSourceGroup;
    resolved.defaultPublicProfileEnabled = resolved.defaultPublicProfileEnabled !== false;
    resolved.defaultPublicProfileId = normalizeString(resolved.defaultPublicProfileId) || DEFAULT_SETTINGS.aggregatorSync.defaultPublicProfileId;
    resolved.defaultPublicProfileCustomId = normalizeString(resolved.defaultPublicProfileCustomId) || DEFAULT_SETTINGS.aggregatorSync.defaultPublicProfileCustomId;
    resolved.defaultPublicProfileName = normalizeString(resolved.defaultPublicProfileName) || DEFAULT_SETTINGS.aggregatorSync.defaultPublicProfileName;
    resolved.defaultPublicProfileDescription = normalizeString(resolved.defaultPublicProfileDescription) || DEFAULT_SETTINGS.aggregatorSync.defaultPublicProfileDescription;
    resolved.defaultPublicProfileConnectorIds = normalizeStringArray(resolved.defaultPublicProfileConnectorIds);
    resolved.enabled = resolved.enabled === true;
    resolved.runOnCron = resolved.runOnCron !== false;
    resolved.autoDisableMissing = resolved.autoDisableMissing !== false;
    resolved.lastSyncAt = normalizeString(resolved.lastSyncAt);
    resolved.lastSyncStatus = normalizeString(resolved.lastSyncStatus) || 'idle';
    resolved.lastSyncMessage = normalizeString(resolved.lastSyncMessage);
    resolved.lastImportedCount = Number.isFinite(Number(resolved.lastImportedCount))
        ? Number(resolved.lastImportedCount)
        : 0;
    resolved.lastDiscoveryImportedCount = Number.isFinite(Number(resolved.lastDiscoveryImportedCount))
        ? Number(resolved.lastDiscoveryImportedCount)
        : 0;
    resolved.lastDiscoveryProbeAt = normalizeString(resolved.lastDiscoveryProbeAt);
    resolved.lastDiscoveryProbedCount = Number.isFinite(Number(resolved.lastDiscoveryProbedCount))
        ? Number(resolved.lastDiscoveryProbedCount)
        : 0;
    resolved.lastDiscoveryVerifiedCount = Number.isFinite(Number(resolved.lastDiscoveryVerifiedCount))
        ? Number(resolved.lastDiscoveryVerifiedCount)
        : 0;
    resolved.lastDiscoveryUnreachableCount = Number.isFinite(Number(resolved.lastDiscoveryUnreachableCount))
        ? Number(resolved.lastDiscoveryUnreachableCount)
        : 0;
    resolved.lastDiscoveryInconclusiveCount = Number.isFinite(Number(resolved.lastDiscoveryInconclusiveCount))
        ? Number(resolved.lastDiscoveryInconclusiveCount)
        : 0;
    resolved.lastDiscoverySkippedCount = Number.isFinite(Number(resolved.lastDiscoverySkippedCount))
        ? Number(resolved.lastDiscoverySkippedCount)
        : 0;

    return resolved;
}

function isManagedDiscoverySource(source) {
    return source?.options?.managed_by === AGGREGATOR_DISCOVERY_MANAGED_BY;
}

function isManagedStableSource(source) {
    return source?.options?.managed_by === AGGREGATOR_STABLE_MANAGED_BY;
}

function isManagedAggregatorSource(source) {
    return isManagedDiscoverySource(source) || isManagedStableSource(source);
}

function findManagedStableSourceIndex(sources) {
    return sources.findIndex(isManagedStableSource);
}

function buildDiscoverySourceName(url, metadata, config, existingSource = null) {
    const existingName = normalizeString(existingSource?.name);
    if (existingName) {
        return existingName;
    }

    let host = 'source';
    try {
        host = new URL(url).hostname || host;
    } catch (_) {
        host = normalizeString(url) || host;
    }

    const origin = normalizeString(metadata?.origin).toUpperCase();
    const parts = [];
    if (config.namePrefix) parts.push(config.namePrefix);
    if (origin) parts.push(origin);
    parts.push(host);
    return parts.join(' · ');
}

function buildDiscoveryNotes(url, metadata, existingSource = null) {
    const existingNotes = normalizeString(existingSource?.notes);
    if (existingNotes) {
        return existingNotes;
    }

    const origin = normalizeString(metadata?.origin).toUpperCase() || 'UNKNOWN';
    const pushTargets = Array.isArray(metadata?.push_to) ? metadata.push_to.join(', ') : '';
    const noteParts = [
        'Managed by aggregator sync',
        'role=internal_discovery',
        `origin=${origin}`
    ];

    if (pushTargets) {
        noteParts.push(`push_to=${pushTargets}`);
    }

    if (metadata?.discovered === true) {
        noteParts.push('discovered=true');
    }

    noteParts.push(`url=${url}`);
    return noteParts.join(' | ');
}

function buildDiscoveryOptions(metadata, existingSource = null) {
    const existingOptions = existingSource?.options && typeof existingSource.options === 'object'
        ? cloneObject(existingSource.options)
        : {};

    return {
        ...existingOptions,
        managed_by: AGGREGATOR_DISCOVERY_MANAGED_BY,
        sync_managed: true,
        source_role: 'internal_discovery',
        visibility: 'internal',
        sync_source: 'aggregator_crawledsubs',
        aggregator_origin: normalizeString(metadata?.origin).toUpperCase(),
        aggregator_push_to: Array.isArray(metadata?.push_to) ? [...metadata.push_to] : [],
        aggregator_rename_rule: normalizeString(metadata?.rename),
        aggregator_include: normalizeString(metadata?.include),
        aggregator_exclude: normalizeString(metadata?.exclude),
        aggregator_defeat: Number.isFinite(Number(metadata?.defeat)) ? Number(metadata.defeat) : 0,
        aggregator_discovered: metadata?.discovered === true
    };
}

function buildStableOptions(existingSource = null) {
    const existingOptions = existingSource?.options && typeof existingSource.options === 'object'
        ? cloneObject(existingSource.options)
        : {};

    return {
        ...existingOptions,
        managed_by: AGGREGATOR_STABLE_MANAGED_BY,
        sync_managed: true,
        source_role: 'stable_output',
        visibility: 'public_default',
        sync_source: 'aggregator_stable_output'
    };
}

function resolveManagedPublicConnectorIds(config, existingProfile, sources) {
    const configuredIds = normalizeStringArray(config.defaultPublicProfileConnectorIds);
    if (configuredIds.length > 0) {
        return configuredIds;
    }

    const sourceById = new Map(
        normalizeSourceCollection(sources).map(source => [source.id, source])
    );

    return normalizeStringArray(existingProfile?.manualNodes).filter(id => {
        const source = sourceById.get(id);
        return Boolean(source) && isConnectorSource(source);
    });
}

function buildManagedProfile(stableSourceId, connectorIds, config, existingProfile = null) {
    const profile = existingProfile && typeof existingProfile === 'object'
        ? cloneObject(existingProfile)
        : {};

    return {
        id: config.defaultPublicProfileId,
        name: config.defaultPublicProfileName,
        enabled: true,
        subscriptions: stableSourceId ? [stableSourceId] : [],
        manualNodes: normalizeStringArray(connectorIds),
        customId: config.defaultPublicProfileCustomId,
        expiresAt: '',
        isPublic: true,
        description: config.defaultPublicProfileDescription,
        prefixSettings: {
            enableManualNodes: null,
            enableSubscriptions: null,
            manualNodePrefix: '',
            prependGroupName: null,
            ...(profile.prefixSettings && typeof profile.prefixSettings === 'object'
                ? profile.prefixSettings
                : {})
        },
        nodeTransform: profile.nodeTransform ?? null,
        options: {
            ...(profile.options && typeof profile.options === 'object' ? profile.options : {}),
            managed_by: AGGREGATOR_PUBLIC_PROFILE_MANAGED_BY,
            sync_managed: true,
            source_role: 'public_stable_profile'
        }
    };
}

async function fetchAggregatorExport(sourceUrl, fetchImpl = fetch) {
    const response = await fetchImpl(sourceUrl, {
        method: 'GET',
        headers: {
            Accept: 'application/json',
            'User-Agent': 'MiSub Aggregator Sync/1.0'
        }
    });

    if (!response.ok) {
        throw new Error(`Aggregator export fetch failed with HTTP ${response.status}`);
    }

    const payload = await response.json();
    if (!payload || Array.isArray(payload) || typeof payload !== 'object') {
        throw new Error('Aggregator export format is invalid');
    }

    return payload;
}

function buildSourceIndex(sources) {
    const index = new Map();
    for (let i = 0; i < sources.length; i += 1) {
        index.set(getSourceKey(sources[i]), i);
    }
    return index;
}

export async function syncAggregatorArtifacts({
    sources = [],
    profiles = [],
    settings = {},
    fetchImpl = fetch
} = {}) {
    const config = resolveAggregatorSyncSettings(settings);
    const now = new Date().toISOString();

    if (!config.enabled) {
        return {
            changedSources: false,
            changedProfiles: false,
            sources: normalizeSourceCollection(sources),
            profiles: Array.isArray(profiles) ? profiles : [],
            summary: {
                ran: false,
                status: 'skipped',
                message: 'Aggregator sync is disabled',
                totalRemote: 0,
                discoveryCreated: 0,
                discoveryUpdated: 0,
                discoveryDisabledMissing: 0,
                stableCreated: 0,
                stableUpdated: 0,
                publicProfileUpdated: 0,
                preservedDuplicates: 0,
                importedCount: 0,
                discoveryImportedCount: 0,
                discoveryProbedCount: 0,
                discoveryVerifiedCount: 0,
                discoveryUnreachableCount: 0,
                discoveryInconclusiveCount: 0,
                discoverySkippedCount: 0
            },
            nextSettings: {
                ...settings,
                aggregatorSync: {
                    ...config,
                    lastSyncAt: now,
                    lastSyncStatus: 'skipped',
                    lastSyncMessage: 'Aggregator sync is disabled',
                    lastImportedCount: 0,
                    lastDiscoveryImportedCount: 0,
                    lastDiscoveryProbeAt: '',
                    lastDiscoveryProbedCount: 0,
                    lastDiscoveryVerifiedCount: 0,
                    lastDiscoveryUnreachableCount: 0,
                    lastDiscoveryInconclusiveCount: 0,
                    lastDiscoverySkippedCount: 0
                }
            }
        };
    }

    const normalizedSources = normalizeSourceCollection(sources);
    const nextSources = [...normalizedSources];
    const nextProfiles = Array.isArray(profiles) ? profiles.map(profile => cloneObject(profile)) : [];
    const sourceIndexByKey = buildSourceIndex(nextSources);
    const remoteMap = await fetchAggregatorExport(config.sourceUrl, fetchImpl);
    const seenDiscoveryKeys = new Set();

    let changedSources = false;
    let changedProfiles = false;
    let discoveryCreated = 0;
    let discoveryUpdated = 0;
    let discoveryDisabledMissing = 0;
    let stableCreated = 0;
    let stableUpdated = 0;
    let publicProfileUpdated = 0;
    let preservedDuplicates = 0;
    let discoveryProbedCount = 0;
    let discoveryVerifiedCount = 0;
    let discoveryUnreachableCount = 0;
    let discoveryInconclusiveCount = 0;
    let discoverySkippedCount = 0;

    for (const [url, rawMetadata] of Object.entries(remoteMap)) {
        const candidate = normalizeSourceCollection([{
            id: '',
            kind: 'subscription',
            input: url,
            url,
            name: '',
            enabled: true,
            group: config.managedGroup,
            notes: '',
            options: {}
        }])[0];

        if (!candidate?.input) {
            continue;
        }

        const metadata = rawMetadata && typeof rawMetadata === 'object' ? rawMetadata : {};
        const key = getSourceKey(candidate);
        const existingIndex = sourceIndexByKey.get(key);
        seenDiscoveryKeys.add(key);

        if (existingIndex === undefined) {
            nextSources.push({
                id: generateManagedId('sub'),
                kind: 'subscription',
                input: candidate.input,
                url: candidate.input,
                name: buildDiscoverySourceName(candidate.input, metadata, config),
                enabled: true,
                group: config.managedGroup,
                notes: buildDiscoveryNotes(candidate.input, metadata),
                options: buildDiscoveryOptions(metadata)
            });
            sourceIndexByKey.set(key, nextSources.length - 1);
            discoveryCreated += 1;
            changedSources = true;
            continue;
        }

        const existingSource = nextSources[existingIndex];
        if (!isManagedDiscoverySource(existingSource)) {
            preservedDuplicates += 1;
            continue;
        }

        const updatedSource = {
            ...existingSource,
            kind: 'subscription',
            input: candidate.input,
            url: candidate.input,
            enabled: existingSource.enabled !== false,
            group: config.managedGroup,
            name: buildDiscoverySourceName(candidate.input, metadata, config, existingSource),
            notes: buildDiscoveryNotes(candidate.input, metadata, existingSource),
            options: {
                ...buildDiscoveryOptions(metadata, existingSource),
                aggregator_missing: false
            }
        };

        if (JSON.stringify(updatedSource) !== JSON.stringify(existingSource)) {
            nextSources[existingIndex] = updatedSource;
            discoveryUpdated += 1;
            changedSources = true;
        }
    }

    if (config.autoDisableMissing) {
        for (let index = 0; index < nextSources.length; index += 1) {
            const source = nextSources[index];
            if (!isManagedDiscoverySource(source)) {
                continue;
            }
            const key = getSourceKey(source);
            if (seenDiscoveryKeys.has(key)) {
                continue;
            }

            if (source.enabled !== false || source?.options?.aggregator_missing !== true) {
                nextSources[index] = {
                    ...source,
                    enabled: false,
                    options: {
                        ...(source.options || {}),
                        aggregator_missing: true
                    }
                };
                discoveryDisabledMissing += 1;
                changedSources = true;
            }
        }
    }

    if (config.secondaryProbeEnabled) {
        const discoveryProbeTargets = nextSources.filter(source =>
            isManagedDiscoverySource(source) &&
            source?.enabled !== false &&
            source?.options?.aggregator_missing !== true
        );

        if (discoveryProbeTargets.length > 0) {
            const probedSources = await probeSourceItems(discoveryProbeTargets, {
                concurrency: 4,
                fetchImpl
            });
            const probedById = new Map(probedSources.map(source => [source.id, source]));

            for (let index = 0; index < nextSources.length; index += 1) {
                const currentSource = nextSources[index];
                const probedSource = probedById.get(currentSource?.id);
                if (!probedSource) {
                    continue;
                }

                if (JSON.stringify(currentSource) !== JSON.stringify(probedSource)) {
                    nextSources[index] = probedSource;
                    changedSources = true;
                }
            }

            discoveryProbedCount = probedSources.length;
            discoveryVerifiedCount = probedSources.filter(source => source?.probe_status === 'verified').length;
            discoveryUnreachableCount = probedSources.filter(source => source?.probe_status === 'unreachable').length;
            discoveryInconclusiveCount = probedSources.filter(source => source?.probe_status === 'inconclusive').length;
            discoverySkippedCount = probedSources.filter(source => source?.probe_status === 'skipped').length;
        }
    }

    let stableSourceId = '';
    if (config.stableSourceEnabled && config.stableSourceUrl) {
        const stableCandidate = normalizeSourceCollection([{
            id: '',
            kind: 'subscription',
            input: config.stableSourceUrl,
            url: config.stableSourceUrl,
            name: config.stableSourceName,
            enabled: true,
            group: config.stableSourceGroup,
            notes: 'Managed aggregator stable output source',
            options: {}
        }])[0];

        const existingManagedStableIndex = findManagedStableSourceIndex(nextSources);
        if (existingManagedStableIndex === -1) {
            const stableSource = {
                id: 'sub_aggregator_stable',
                kind: 'subscription',
                input: stableCandidate.input,
                url: stableCandidate.input,
                name: config.stableSourceName,
                enabled: true,
                group: config.stableSourceGroup,
                notes: 'Managed by MiSub aggregator integration | role=stable_output',
                options: buildStableOptions()
            };
            nextSources.unshift(stableSource);
            stableSourceId = stableSource.id;
            stableCreated += 1;
            changedSources = true;
        } else {
            const existingStableSource = nextSources[existingManagedStableIndex];
            stableSourceId = existingStableSource.id;

            const updatedStable = {
                ...existingStableSource,
                kind: 'subscription',
                input: stableCandidate.input,
                url: stableCandidate.input,
                enabled: true,
                group: config.stableSourceGroup,
                name: config.stableSourceName,
                notes: 'Managed by MiSub aggregator integration | role=stable_output',
                options: buildStableOptions(existingStableSource)
            };

            if (JSON.stringify(updatedStable) !== JSON.stringify(existingStableSource)) {
                nextSources[existingManagedStableIndex] = updatedStable;
                stableUpdated += 1;
                changedSources = true;
            }
        }
    }

    if (config.stableSourceEnabled && config.stableSourceUrl && !stableSourceId) {
        const stableIndex = findManagedStableSourceIndex(nextSources);
        if (stableIndex !== -1) {
            stableSourceId = nextSources[stableIndex].id;
        }
    }

    if (config.defaultPublicProfileEnabled && stableSourceId) {
        const profileIndex = nextProfiles.findIndex(profile =>
            profile?.id === config.defaultPublicProfileId ||
            profile?.customId === config.defaultPublicProfileCustomId ||
            profile?.options?.managed_by === AGGREGATOR_PUBLIC_PROFILE_MANAGED_BY
        );

        const existingProfile = profileIndex >= 0 ? nextProfiles[profileIndex] : null;
        const connectorIds = resolveManagedPublicConnectorIds(config, existingProfile, nextSources);
        const managedProfile = buildManagedProfile(stableSourceId, connectorIds, config, existingProfile);

        if (profileIndex >= 0) {
            if (JSON.stringify(nextProfiles[profileIndex]) !== JSON.stringify(managedProfile)) {
                nextProfiles[profileIndex] = managedProfile;
                publicProfileUpdated += 1;
                changedProfiles = true;
            }
        } else {
            nextProfiles.unshift(managedProfile);
            publicProfileUpdated += 1;
            changedProfiles = true;
        }
    }

    const totalRemote = Object.keys(remoteMap).length;
    const importedCount = nextSources.filter(isManagedAggregatorSource).length;
    const discoveryImportedCount = nextSources.filter(isManagedDiscoverySource).length;
    const messageParts = [
        `remote=${totalRemote}`,
        `discoveryCreated=${discoveryCreated}`,
        `discoveryUpdated=${discoveryUpdated}`,
        `stableCreated=${stableCreated}`,
        `stableUpdated=${stableUpdated}`,
        `publicProfileUpdated=${publicProfileUpdated}`,
        `duplicates=${preservedDuplicates}`,
        `reProbed=${discoveryProbedCount}`,
        `reVerified=${discoveryVerifiedCount}`
    ];
    if (config.autoDisableMissing) {
        messageParts.push(`disabledMissing=${discoveryDisabledMissing}`);
    }

    return {
        changedSources,
        changedProfiles,
        changed: changedSources || changedProfiles,
        sources: normalizeSourceCollection(nextSources),
        profiles: nextProfiles,
        summary: {
            ran: true,
            status: 'success',
            message: messageParts.join(', '),
            totalRemote,
            discoveryCreated,
            discoveryUpdated,
            discoveryDisabledMissing,
            stableCreated,
            stableUpdated,
            publicProfileUpdated,
            preservedDuplicates,
            importedCount,
            discoveryImportedCount,
            discoveryProbedCount,
            discoveryVerifiedCount,
            discoveryUnreachableCount,
            discoveryInconclusiveCount,
            discoverySkippedCount
        },
        nextSettings: {
            ...settings,
            aggregatorSync: {
                ...config,
                lastSyncAt: now,
                lastSyncStatus: 'success',
                lastSyncMessage: messageParts.join(', '),
                lastImportedCount: importedCount,
                lastDiscoveryImportedCount: discoveryImportedCount,
                lastDiscoveryProbeAt: config.secondaryProbeEnabled ? now : '',
                lastDiscoveryProbedCount: discoveryProbedCount,
                lastDiscoveryVerifiedCount: discoveryVerifiedCount,
                lastDiscoveryUnreachableCount: discoveryUnreachableCount,
                lastDiscoveryInconclusiveCount: discoveryInconclusiveCount,
                lastDiscoverySkippedCount: discoverySkippedCount
            }
        }
    };
}

export async function runAggregatorSync(env, storageAdapter, settingsOverride = null, fetchImpl = fetch) {
    const [storedSources, storedProfiles, storedSettings] = await Promise.all([
        storageAdapter.get(KV_KEY_SUBS).then(result => result || []),
        storageAdapter.get(KV_KEY_PROFILES).then(result => result || []),
        storageAdapter.get(KV_KEY_SETTINGS).then(result => result || {})
    ]);

    const effectiveSettings = settingsOverride
        ? {
            ...storedSettings,
            ...settingsOverride,
            aggregatorSync: {
                ...resolveAggregatorSyncSettings(storedSettings),
                ...cloneObject(settingsOverride?.aggregatorSync)
            }
        }
        : storedSettings;

    try {
        const result = await syncAggregatorArtifacts({
            sources: storedSources,
            profiles: storedProfiles,
            settings: effectiveSettings,
            fetchImpl
        });

        if (result.changedSources) {
            await storageAdapter.put(KV_KEY_SUBS, result.sources);
        }
        if (result.changedProfiles) {
            await storageAdapter.put(KV_KEY_PROFILES, result.profiles);
        }
        await storageAdapter.put(KV_KEY_SETTINGS, result.nextSettings);
        SettingsCache.clear();
        return result;
    } catch (error) {
        const failedSettings = {
            ...storedSettings,
            aggregatorSync: {
                ...resolveAggregatorSyncSettings(effectiveSettings),
                lastSyncAt: new Date().toISOString(),
                lastSyncStatus: 'error',
                lastSyncMessage: normalizeString(error?.message) || 'Aggregator sync failed',
                lastImportedCount: resolveAggregatorSyncSettings(storedSettings).lastImportedCount || 0,
                lastDiscoveryImportedCount: resolveAggregatorSyncSettings(storedSettings).lastDiscoveryImportedCount || 0,
                lastDiscoveryProbeAt: resolveAggregatorSyncSettings(storedSettings).lastDiscoveryProbeAt || '',
                lastDiscoveryProbedCount: resolveAggregatorSyncSettings(storedSettings).lastDiscoveryProbedCount || 0,
                lastDiscoveryVerifiedCount: resolveAggregatorSyncSettings(storedSettings).lastDiscoveryVerifiedCount || 0,
                lastDiscoveryUnreachableCount: resolveAggregatorSyncSettings(storedSettings).lastDiscoveryUnreachableCount || 0,
                lastDiscoveryInconclusiveCount: resolveAggregatorSyncSettings(storedSettings).lastDiscoveryInconclusiveCount || 0,
                lastDiscoverySkippedCount: resolveAggregatorSyncSettings(storedSettings).lastDiscoverySkippedCount || 0
            }
        };
        await storageAdapter.put(KV_KEY_SETTINGS, failedSettings);
        SettingsCache.clear();
        throw error;
    }
}
