const ISSUE91_PATH = "/issue91";
const USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36";

function decodeBase64Utf8(value) {
  try {
    return new TextDecoder().decode(Uint8Array.from(atob(value), (ch) => ch.charCodeAt(0))).trim();
  } catch (error) {
    throw new Error(`failed to decode ISSUE91_UPSTREAM_URL_B64: ${error instanceof Error ? error.message : String(error)}`);
  }
}

function resolveUpstreamUrl(env) {
  const encoded = String(env.ISSUE91_UPSTREAM_URL_B64 || "").trim();
  if (!encoded) {
    throw new Error("missing ISSUE91_UPSTREAM_URL_B64 secret");
  }
  const decoded = decodeBase64Utf8(encoded);
  if (!decoded.startsWith("http://") && !decoded.startsWith("https://")) {
    throw new Error("decoded ISSUE91 upstream URL is not an absolute HTTP(S) URL");
  }
  return decoded;
}

async function fetchIssue91Payload(upstreamUrl) {
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
    throw new Error(`upstream returned ${response.status}: ${body.slice(0, 200)}`);
  }

  if (!body.includes("proxies:")) {
    throw new Error(`upstream body does not contain a Clash proxies list: ${body.slice(0, 200)}`);
  }

  return body;
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
      const upstreamUrl = resolveUpstreamUrl(env);
      const body = await fetchIssue91Payload(upstreamUrl);
      return new Response(body, {
        status: 200,
        headers: {
          "content-type": "text/yaml; charset=utf-8",
          "cache-control": "public, max-age=60",
          "x-easyproxy-relay": "issue91",
        },
      });
    } catch (error) {
      return jsonResponse(502, {
        ok: false,
        error: "upstream_fetch_failed",
        message: error instanceof Error ? error.message : String(error),
      });
    }
  },
};
