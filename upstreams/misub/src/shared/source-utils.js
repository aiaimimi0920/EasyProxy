/**
 * Shared source helpers used by both MiSub frontend and backend.
 * They normalize legacy `{ url }` records into the new `{ kind, input }` model
 * while keeping `url` mirrored for backward compatibility.
 */

export const SOURCE_KIND_SUBSCRIPTION = 'subscription';
export const SOURCE_KIND_PROXY_URI = 'proxy_uri';
export const SOURCE_KIND_CONNECTOR = 'connector';
export const SOURCE_CONNECTOR_TYPE_ECH_WORKER = 'ech_worker';
export const SOURCE_DETECTED_KIND_UNKNOWN = 'unknown';

export const SOURCE_PROBE_STATUS_UNCHECKED = 'unchecked';
export const SOURCE_PROBE_STATUS_SKIPPED = 'skipped';
export const SOURCE_PROBE_STATUS_VERIFIED = 'verified';
export const SOURCE_PROBE_STATUS_UNREACHABLE = 'unreachable';
export const SOURCE_PROBE_STATUS_INCONCLUSIVE = 'inconclusive';

const DEFAULT_DIRECT_PROXY_SCHEME = 'http';

const PROXY_URI_SCHEMES = new Set([
    'ss',
    'ssr',
    'vmess',
    'vless',
    'trojan',
    'hysteria',
    'hysteria2',
    'hy',
    'hy2',
    'tuic',
    'anytls',
    'socks5',
    'socks',
    'snell',
    'naive+https',
    'naive+quic',
    'naive+http',
    'http',
    'https',
    'wireguard'
]);

const BARE_PROXY_INPUT_REGEX = /^(?<auth>[^@\s]+@)?(?<host>\[[^\]]+\]|[^:\s/]+):(?<port>\d{1,5})$/i;
const SAFE_URL_SCHEME_REGEX = /^[a-z][a-z0-9+.-]*:\/\//i;
const VALID_PROBE_STATUSES = new Set([
    SOURCE_PROBE_STATUS_UNCHECKED,
    SOURCE_PROBE_STATUS_SKIPPED,
    SOURCE_PROBE_STATUS_VERIFIED,
    SOURCE_PROBE_STATUS_UNREACHABLE,
    SOURCE_PROBE_STATUS_INCONCLUSIVE
]);
const VALID_DETECTED_KINDS = new Set([
    SOURCE_KIND_SUBSCRIPTION,
    SOURCE_KIND_PROXY_URI,
    SOURCE_KIND_CONNECTOR,
    SOURCE_DETECTED_KIND_UNKNOWN
]);

function normalizeString(value) {
    if (typeof value !== 'string') return '';
    return value.trim();
}

function safeURLParse(value) {
    try {
        return new URL(value);
    } catch (_) {
        return null;
    }
}

function extractProtocol(value) {
    const match = normalizeString(value).match(/^([a-z][a-z0-9+.-]*):\/\//i);
    return match ? match[1].toLowerCase() : '';
}

export function isBareProxyInput(value) {
    const trimmed = normalizeString(value);
    if (!trimmed) return false;
    if (SAFE_URL_SCHEME_REGEX.test(trimmed)) return false;
    return BARE_PROXY_INPUT_REGEX.test(trimmed);
}

export function normalizeDirectProxyInput(value, defaultScheme = DEFAULT_DIRECT_PROXY_SCHEME) {
    const trimmed = normalizeString(value);
    if (!trimmed) return '';
    if (isBareProxyInput(trimmed)) {
        return `${normalizeString(defaultScheme) || DEFAULT_DIRECT_PROXY_SCHEME}://${trimmed}`;
    }
    return trimmed;
}

export function isLikelyHTTPProxyInput(value) {
    const trimmed = normalizeString(value);
    if (!trimmed) return false;

    if (isBareProxyInput(trimmed)) {
        return true;
    }

    const parsed = safeURLParse(trimmed);
    if (!parsed) return false;
    if (!['http:', 'https:'].includes(parsed.protocol)) return false;

    const hasCredentials = Boolean(parsed.username || parsed.password);
    const hasPort = Boolean(parsed.port);
    const pathname = parsed.pathname || '';
    const hasOnlyRootPath = pathname === '' || pathname === '/';
    const hasQueryOrHash = Boolean(parsed.search || parsed.hash);

    return hasCredentials || (hasPort && hasOnlyRootPath && !hasQueryOrHash);
}

export function isSubscriptionInput(value) {
    const trimmed = normalizeString(value);
    const parsed = safeURLParse(trimmed);
    if (!parsed) return false;
    if (!['http:', 'https:'].includes(parsed.protocol)) return false;
    return !isLikelyHTTPProxyInput(trimmed);
}

export function isProxyURIInput(value) {
    const normalized = normalizeDirectProxyInput(value);
    if (!normalized) return false;
    const protocol = extractProtocol(normalized);
    return PROXY_URI_SCHEMES.has(protocol);
}

export function getSourceInput(item) {
    return normalizeSourceItem(item).input;
}

export function inferSourceKind(value, explicitKind = '') {
    const normalizedKind = normalizeString(explicitKind);
    if (normalizedKind === SOURCE_KIND_CONNECTOR) return SOURCE_KIND_CONNECTOR;
    if (normalizedKind === SOURCE_KIND_SUBSCRIPTION) return SOURCE_KIND_SUBSCRIPTION;
    if (normalizedKind === SOURCE_KIND_PROXY_URI) return SOURCE_KIND_PROXY_URI;

    const normalizedValue = normalizeDirectProxyInput(value);
    if (!normalizedValue) return SOURCE_KIND_PROXY_URI;
    if (isSubscriptionInput(normalizedValue)) return SOURCE_KIND_SUBSCRIPTION;
    if (isProxyURIInput(normalizedValue)) return SOURCE_KIND_PROXY_URI;
    return SOURCE_KIND_PROXY_URI;
}

function mergeSourceOptions(item, kind) {
    const existingOptions = item && typeof item.options === 'object' && item.options !== null
        ? { ...item.options }
        : {};

    if (kind === SOURCE_KIND_SUBSCRIPTION) {
        if (normalizeString(item?.exclude)) existingOptions.exclude = item.exclude;
        if (normalizeString(item?.customUserAgent)) existingOptions.customUserAgent = item.customUserAgent;
        if (normalizeString(item?.fetchProxy)) existingOptions.fetchProxy = item.fetchProxy;
        if (item?.plusAsSpace === true) existingOptions.plusAsSpace = true;
    }

    if (kind === SOURCE_KIND_CONNECTOR) {
        if (normalizeString(item?.connector_type)) existingOptions.connector_type = item.connector_type;
        if (item?.connector_config && typeof item.connector_config === 'object') {
            existingOptions.connector_config = item.connector_config;
        }
    }

    return existingOptions;
}

function normalizeSourceProbeMetadata(source, normalizedInput) {
    const probeInput = normalizeString(source.probe_input);
    const hasFreshProbe = probeInput === normalizedInput;
    let probeStatus = normalizeString(source.probe_status);
    if (!VALID_PROBE_STATUSES.has(probeStatus)) {
        probeStatus = SOURCE_PROBE_STATUS_UNCHECKED;
    }

    let detectedKind = normalizeString(source.detected_kind);
    if (!VALID_DETECTED_KINDS.has(detectedKind)) {
        detectedKind = SOURCE_DETECTED_KIND_UNKNOWN;
    }

    if (!hasFreshProbe) {
        return {
            probe_status: SOURCE_PROBE_STATUS_UNCHECKED,
            detected_kind: SOURCE_DETECTED_KIND_UNKNOWN,
            last_probe_at: '',
            probe_message: '',
            probe_input: ''
        };
    }

    if (probeStatus === SOURCE_PROBE_STATUS_UNCHECKED) {
        return {
            probe_status: SOURCE_PROBE_STATUS_UNCHECKED,
            detected_kind: SOURCE_DETECTED_KIND_UNKNOWN,
            last_probe_at: '',
            probe_message: '',
            probe_input: ''
        };
    }

    return {
        probe_status: probeStatus,
        detected_kind: detectedKind,
        last_probe_at: normalizeString(source.last_probe_at),
        probe_message: normalizeString(source.probe_message),
        probe_input: probeInput
    };
}

export function normalizeSourceItem(item, options = {}) {
    const source = item && typeof item === 'object' ? { ...item } : {};
    const defaultDirectProxyScheme = normalizeString(options.defaultDirectProxyScheme) || DEFAULT_DIRECT_PROXY_SCHEME;
    const rawInput = normalizeString(source.input || source.url);
    const normalizedInput = normalizeDirectProxyInput(rawInput, defaultDirectProxyScheme);
    const kind = inferSourceKind(normalizedInput, source.kind);
    const normalized = {
        ...source,
        kind,
        input: normalizedInput,
        url: normalizedInput,
        name: normalizeString(source.name) || '',
        enabled: source.enabled !== false,
        group: normalizeString(source.group),
        notes: normalizeString(source.notes),
        options: mergeSourceOptions(source, kind),
        ...normalizeSourceProbeMetadata(source, normalizedInput)
    };

    if (kind !== SOURCE_KIND_SUBSCRIPTION) {
        normalized.exclude = '';
        normalized.customUserAgent = '';
        normalized.fetchProxy = '';
        normalized.plusAsSpace = false;
    } else {
        normalized.exclude = normalizeString(source.exclude);
        normalized.customUserAgent = normalizeString(source.customUserAgent);
        normalized.fetchProxy = normalizeString(source.fetchProxy);
        normalized.plusAsSpace = source.plusAsSpace === true;
    }

    if (kind === SOURCE_KIND_CONNECTOR) {
        normalized.connector_type = normalizeString(
            source.connector_type || normalized.options?.connector_type
        );
        normalized.connector_config =
            normalized.options?.connector_config && typeof normalized.options.connector_config === 'object'
                ? { ...normalized.options.connector_config }
                : {};
    } else {
        normalized.connector_type = '';
        normalized.connector_config = {};
    }

    return normalized;
}

export function normalizeSourceCollection(items, options = {}) {
    if (!Array.isArray(items)) return [];
    return items.map(item => normalizeSourceItem(item, options));
}

export function isSubscriptionSource(item) {
    return normalizeSourceItem(item).kind === SOURCE_KIND_SUBSCRIPTION;
}

export function isProxyURISource(item) {
    return normalizeSourceItem(item).kind === SOURCE_KIND_PROXY_URI;
}

export function isConnectorSource(item) {
    return normalizeSourceItem(item).kind === SOURCE_KIND_CONNECTOR;
}

export function getConnectorType(item) {
    return normalizeSourceItem(item).connector_type || '';
}

export function isEchWorkerSource(item) {
    const normalized = normalizeSourceItem(item);
    return normalized.kind === SOURCE_KIND_CONNECTOR && normalized.connector_type === SOURCE_CONNECTOR_TYPE_ECH_WORKER;
}

export function getSourceKey(item) {
    const normalized = normalizeSourceItem(item);
    if (normalized.kind === SOURCE_KIND_CONNECTOR) {
        const connectorOptions = {
            connector_type: normalized.connector_type || '',
            connector_config: normalized.connector_config && typeof normalized.connector_config === 'object'
                ? Object.keys(normalized.connector_config)
                    .sort()
                    .reduce((acc, key) => {
                        acc[key] = normalized.connector_config[key];
                        return acc;
                    }, {})
                : {}
        };
        return `${normalized.kind}:${normalized.input}:${JSON.stringify(connectorOptions)}`;
    }
    return `${normalized.kind}:${normalized.input}`;
}

export function dedupeSources(items) {
    const seen = new Set();
    const deduped = [];

    for (const item of normalizeSourceCollection(items)) {
        const key = getSourceKey(item);
        if (seen.has(key)) continue;
        seen.add(key);
        deduped.push(item);
    }

    return deduped;
}

export function shouldProbeSource(item) {
    const normalized = normalizeSourceItem(item);
    if (!normalized.input) return false;
    if (![SOURCE_KIND_SUBSCRIPTION, SOURCE_KIND_PROXY_URI].includes(normalized.kind)) {
        return false;
    }
    const lowerInput = normalized.input.toLowerCase();
    if (!lowerInput.startsWith('http://') && !lowerInput.startsWith('https://')) {
        return false;
    }
    return !isLikelyHTTPProxyInput(normalized.input);
}

export function canManuallyProbeSource(item) {
    const normalized = normalizeSourceItem(item);
    if (!normalized.input) return false;
    return shouldProbeSource(normalized) || isLikelyHTTPProxyInput(normalized.input);
}

export function isSourceProbeMismatch(item) {
    const normalized = normalizeSourceItem(item);
    if (!normalized.detected_kind || normalized.detected_kind === SOURCE_DETECTED_KIND_UNKNOWN) {
        return false;
    }
    return normalized.detected_kind !== normalized.kind;
}

export function getSourceProbeSummary(item) {
    const normalized = normalizeSourceItem(item);

    if (normalized.probe_status === SOURCE_PROBE_STATUS_SKIPPED) {
        return {
            status: normalized.probe_status,
            label: normalized.detected_kind === SOURCE_KIND_PROXY_URI ? '结构像代理' : '结构已判定',
            tone: 'muted',
            mismatch: isSourceProbeMismatch(normalized),
            description: normalized.probe_message || '输入结构已经足够明确，未执行联网探测。'
        };
    }

    if (normalized.probe_status === SOURCE_PROBE_STATUS_VERIFIED) {
        if (isSourceProbeMismatch(normalized)) {
            return {
                status: normalized.probe_status,
                label: normalized.detected_kind === SOURCE_KIND_PROXY_URI ? '探测疑似代理' : '探测疑似订阅',
                tone: 'warning',
                mismatch: true,
                description: normalized.probe_message || '联网探测结果与当前声明类型不一致，请手动确认。'
            };
        }

        return {
            status: normalized.probe_status,
            label: '探测一致',
            tone: 'success',
            mismatch: false,
            description: normalized.probe_message || '联网探测结果与当前声明类型一致。'
        };
    }

    if (normalized.probe_status === SOURCE_PROBE_STATUS_UNREACHABLE) {
        return {
            status: normalized.probe_status,
            label: '探测失败',
            tone: 'danger',
            mismatch: false,
            description: normalized.probe_message || '本次没有成功拉取到可判断的内容。'
        };
    }

    if (normalized.probe_status === SOURCE_PROBE_STATUS_INCONCLUSIVE) {
        return {
            status: normalized.probe_status,
            label: '探测未定',
            tone: 'warning',
            mismatch: false,
            description: normalized.probe_message || '本次拉取到了内容，但还不足以可靠判断类型。'
        };
    }

    return {
        status: SOURCE_PROBE_STATUS_UNCHECKED,
        label: '未探测',
        tone: 'muted',
        mismatch: false,
        description: '保存后会对歧义 http(s) 来源做一次辅助探测，但不会自动改写类型。'
    };
}

export function shouldShowSourceProbeNotice(item) {
    const summary = getSourceProbeSummary(item);
    return summary.mismatch
        || summary.status === SOURCE_PROBE_STATUS_UNREACHABLE
        || summary.status === SOURCE_PROBE_STATUS_INCONCLUSIVE;
}

export function toManifestSource(item) {
    const normalized = normalizeSourceItem(item);
    return {
        id: normalized.id,
        kind: normalized.kind,
        name: normalized.name,
        enabled: normalized.enabled,
        group: normalized.group,
        notes: normalized.notes,
        input: normalized.input,
        options: normalized.options || {}
    };
}
