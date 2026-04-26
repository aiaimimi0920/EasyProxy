import {
    SOURCE_DETECTED_KIND_UNKNOWN,
    SOURCE_KIND_PROXY_URI,
    SOURCE_KIND_SUBSCRIPTION,
    SOURCE_PROBE_STATUS_INCONCLUSIVE,
    SOURCE_PROBE_STATUS_SKIPPED,
    SOURCE_PROBE_STATUS_UNREACHABLE,
    SOURCE_PROBE_STATUS_VERIFIED,
    isLikelyHTTPProxyInput,
    normalizeSourceItem,
    shouldProbeSource
} from '../../src/shared/source-utils.js';

const PROBE_TIMEOUT_MS = 6000;
const DEFAULT_BATCH_CONCURRENCY = 4;
const KNOWN_NODE_SCHEME_REGEX = /^(ss|ssr|vmess|vless|trojan|hysteria|hysteria2|hy2|tuic|anytls|wireguard|socks5|snell|naive\+https|naive\+http|naive\+quic):\/\/\S+/im;

function withProbeMetadata(source, metadata = {}) {
    return normalizeSourceItem({
        ...source,
        probe_status: metadata.probe_status,
        detected_kind: metadata.detected_kind,
        last_probe_at: metadata.last_probe_at,
        probe_message: metadata.probe_message,
        probe_input: metadata.probe_input
    });
}

function isReusableProbe(previous, current) {
    const normalizedPrevious = normalizeSourceItem(previous);
    const normalizedCurrent = normalizeSourceItem(current);
    if (!normalizedPrevious.last_probe_at) return false;
    if (normalizedPrevious.input !== normalizedCurrent.input) return false;
    if (normalizedPrevious.kind !== normalizedCurrent.kind) return false;
    return Boolean(normalizedPrevious.probe_status && normalizedPrevious.probe_status !== 'unchecked');
}

function looksLikeClashPayload(text) {
    return /(^|\n)\s*proxies:\s*(\n|\[)/i.test(text) || /(^|\n)\s*proxy-groups:\s*(\n|\[)/i.test(text);
}

function looksLikeSingBoxPayload(text) {
    try {
        const parsed = JSON.parse(text);
        return Array.isArray(parsed?.outbounds) || Array.isArray(parsed?.proxies);
    } catch (_) {
        return false;
    }
}

function looksLikePlainNodeList(text) {
    const lines = String(text || '')
        .split('\n')
        .map(line => line.trim())
        .filter(Boolean);
    return lines.some(line => KNOWN_NODE_SCHEME_REGEX.test(line));
}

function tryDecodeBase64(text) {
    const trimmed = String(text || '').replace(/\s+/g, '');
    if (!trimmed || trimmed.includes('://')) return '';
    if (!/^[A-Za-z0-9+/=_-]+$/.test(trimmed)) return '';

    try {
        return atob(trimmed);
    } catch (_) {
        return '';
    }
}

function looksLikeSubscriptionPayload(text, contentType = '') {
    const trimmed = String(text || '').trim();
    if (!trimmed) return false;

    const candidates = [trimmed];
    const decoded = tryDecodeBase64(trimmed);
    if (decoded) candidates.push(decoded);

    return candidates.some(candidate => {
        const candidateText = String(candidate || '').trim();
        if (!candidateText) return false;
        if (looksLikePlainNodeList(candidateText)) return true;
        if (looksLikeClashPayload(candidateText)) return true;
        if (looksLikeSingBoxPayload(candidateText)) return true;
        return false;
    }) || /\b(yaml|yml|json|clash|sing-box|base64)\b/i.test(contentType);
}

async function fetchWithTimeout(input, init = {}, timeoutMs = PROBE_TIMEOUT_MS, fetchImpl = fetch) {
    const controller = typeof AbortController !== 'undefined' ? new AbortController() : null;
    let timeoutId = null;

    if (controller) {
        timeoutId = setTimeout(() => controller.abort(), timeoutMs);
    }

    try {
        return await fetchImpl(input, {
            redirect: 'follow',
            ...init,
            signal: controller?.signal
        });
    } finally {
        if (timeoutId) clearTimeout(timeoutId);
    }
}

async function probeSource(source, options = {}) {
    const normalized = normalizeSourceItem(source);
    const now = new Date().toISOString();
    const fetchImpl = typeof options.fetchImpl === 'function' ? options.fetchImpl : fetch;

    if (isLikelyHTTPProxyInput(normalized.input)) {
        return withProbeMetadata(normalized, {
            probe_status: SOURCE_PROBE_STATUS_SKIPPED,
            detected_kind: SOURCE_KIND_PROXY_URI,
            last_probe_at: now,
            probe_message: '输入结构已经明显像 HTTP(S) 直连代理，因此跳过联网探测。',
            probe_input: normalized.input
        });
    }

    if (!shouldProbeSource(normalized)) {
        return normalized;
    }

    try {
        const response = await fetchWithTimeout(normalized.input, {
            method: 'GET',
            headers: {
                'Accept': '*/*',
                'User-Agent': 'MiSub-SourceProbe/1.0'
            }
        }, PROBE_TIMEOUT_MS, fetchImpl);

        const contentType = response.headers.get('content-type') || '';
        const proxyAuthenticate = response.headers.get('proxy-authenticate');

        if (response.status === 407 || proxyAuthenticate) {
            return withProbeMetadata(normalized, {
                probe_status: SOURCE_PROBE_STATUS_VERIFIED,
                detected_kind: SOURCE_KIND_PROXY_URI,
                last_probe_at: now,
                probe_message: '响应表现为代理认证入口，更像直连代理而不是订阅文件。',
                probe_input: normalized.input
            });
        }

        if (!response.ok) {
            return withProbeMetadata(normalized, {
                probe_status: SOURCE_PROBE_STATUS_UNREACHABLE,
                detected_kind: SOURCE_DETECTED_KIND_UNKNOWN,
                last_probe_at: now,
                probe_message: `HTTP ${response.status}，本次无法据此可靠判断类型。`,
                probe_input: normalized.input
            });
        }

        const body = await response.text();
        if (looksLikeSubscriptionPayload(body, contentType)) {
            return withProbeMetadata(normalized, {
                probe_status: SOURCE_PROBE_STATUS_VERIFIED,
                detected_kind: SOURCE_KIND_SUBSCRIPTION,
                last_probe_at: now,
                probe_message: '成功拉取到可解析的订阅型内容。',
                probe_input: normalized.input
            });
        }

        return withProbeMetadata(normalized, {
            probe_status: SOURCE_PROBE_STATUS_INCONCLUSIVE,
            detected_kind: SOURCE_DETECTED_KIND_UNKNOWN,
            last_probe_at: now,
            probe_message: '成功拉取到了响应，但内容不像常见订阅载荷，建议人工确认。',
            probe_input: normalized.input
        });
    } catch (error) {
        return withProbeMetadata(normalized, {
            probe_status: SOURCE_PROBE_STATUS_UNREACHABLE,
            detected_kind: SOURCE_DETECTED_KIND_UNKNOWN,
            last_probe_at: now,
            probe_message: `探测请求失败：${error?.message || 'unknown error'}`,
            probe_input: normalized.input
        });
    }
}

export async function probeSourceItem(item, options = {}) {
    return await probeSource(normalizeSourceItem(item), options);
}

export async function probeSourceItems(items, options = {}) {
    const sources = Array.isArray(items) ? items.map(item => normalizeSourceItem(item)) : [];
    if (sources.length === 0) return [];

    const concurrency = Math.max(1, Number(options.concurrency) || DEFAULT_BATCH_CONCURRENCY);
    const results = new Array(sources.length);
    let cursor = 0;

    const workers = Array.from({ length: Math.min(concurrency, sources.length) }, async () => {
        while (true) {
            const index = cursor++;
            if (index >= sources.length) return;
            results[index] = await probeSourceItem(sources[index], options);
        }
    });

    await Promise.all(workers);
    return results;
}

export async function enrichSourcesWithProbeMetadata(items, previousItems = [], options = {}) {
    const previousById = new Map(
        (Array.isArray(previousItems) ? previousItems : []).map(item => [item?.id, item])
    );

    const results = [];
    for (const item of Array.isArray(items) ? items : []) {
        const normalized = normalizeSourceItem(item);
        const previous = previousById.get(normalized.id);

        if (previous && isReusableProbe(previous, normalized)) {
            results.push(normalizeSourceItem(previous));
            continue;
        }

        results.push(await probeSourceItem(normalized, options));
    }

    return results;
}
