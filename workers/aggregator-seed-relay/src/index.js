const ISSUE91_PATH = "/issue91";
const USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36";

class RelayError extends Error {}

function decodeBase64Utf8(value) {
  try {
    return new TextDecoder().decode(Uint8Array.from(atob(value), (ch) => ch.charCodeAt(0))).trim();
  } catch {
    throw new RelayError("failed to decode ISSUE91_UPSTREAM_URL_B64");
  }
}

function resolveUpstreamUrl(env) {
  const encoded = String(env.ISSUE91_UPSTREAM_URL_B64 || "").trim();
  if (!encoded) {
    throw new RelayError("missing ISSUE91_UPSTREAM_URL_B64 secret");
  }
  const decoded = decodeBase64Utf8(encoded);
  if (!decoded.startsWith("http://") && !decoded.startsWith("https://")) {
    throw new RelayError("decoded ISSUE91 upstream URL is not an absolute HTTP(S) URL");
  }
  return decoded;
}

async function fetchIssue91Payload(upstreamUrl, source) {
  const response = await fetch(upstreamUrl, {
    method: "GET",
    headers: {
      "User-Agent": USER_AGENT,
      Accept: "text/yaml, application/yaml, text/plain, */*",
      "Cache-Control": "no-cache",
      Pragma: "no-cache",
    },
    redirect: "follow",
    cf: {
      cacheTtl: 0,
      cacheEverything: false,
    },
  });

  const body = await response.text();
  if (!response.ok) {
    throw new RelayError(`${source} returned HTTP ${response.status}`);
  }

  if (!body.includes("proxies:")) {
    throw new RelayError(`${source} body does not contain a Clash proxies list`);
  }

  return body;
}

async function fetchIssue91WithFallback(env) {
  const upstreamUrl = resolveUpstreamUrl(env);
  try {
    const body = await fetchIssue91Payload(upstreamUrl, "upstream");
    return { body, source: "upstream" };
  } catch (upstreamError) {
    const cacheUrl = String(env.ISSUE91_CACHE_URL || "").trim();
    if (!cacheUrl) throw upstreamError;
    const cacheBody = await fetchIssue91Payload(cacheUrl, "cache");
    return { body: cacheBody, source: "cache" };
  }
}

function jsonResponse(status, payload) {
  return new Response(JSON.stringify(payload, null, 2), {
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    if (url.pathname === "/" || url.pathname === "") {
      return jsonResponse(200, {
        ok: true,
        service: "easyproxy-aggregator-seed-relay",
        endpoints: [ISSUE91_PATH],
      });
    }

    if (url.pathname !== ISSUE91_PATH) {
      return jsonResponse(404, { ok: false, error: "not_found" });
    }

    try {
      const { body, source } = await fetchIssue91WithFallback(env);
      return new Response(body, {
        status: 200,
        headers: {
          "content-type": "text/yaml; charset=utf-8",
          "cache-control": "public, max-age=60",
          "x-easyproxy-relay": "issue91",
          "x-easyproxy-relay-source": source,
        },
      });
    } catch (error) {
      return jsonResponse(502, {
        ok: false,
        error: "upstream_fetch_failed",
        message: error instanceof RelayError ? error.message : "relay request failed",
      });
    }
  },
};
