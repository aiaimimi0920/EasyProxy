// 私有 R2 的只读发布出口；不暴露控制记录、备份或任意对象键。
const prefix = "/aggregator/";
const publicKeys = new Set([
  "subs/clash.yaml", "subs/singbox.json", "subs/v2ray.txt", "subs/mixed.txt",
  "subs/effective.txt", "subs/effective.json", "internal/crawledsubs.json"
]);
const runId = "[A-Za-z0-9][A-Za-z0-9._-]{0,127}";
const runPrefix = new RegExp(`^(?:candidate|releases)/(${runId})/(.+)$`);
const previousRelease = new RegExp(`^last-known-good/releases/(${runId})/(.+)$`);
const maxObjectBytes = 64 * 1024 * 1024;

export function isAggregatorRoute(pathname) {
  return pathname === "/aggregator" || pathname.startsWith(prefix);
}

export function publicArtifactKey(pathname) {
  if (!pathname.startsWith(prefix) || pathname.length > 1024) return null;
  const key = pathname.slice(prefix.length);
  if (!key || /[^A-Za-z0-9._/-]/.test(key) || key.split("/").some(part => !part || part === "." || part === "..")) return null;
  if (publicKeys.has(key) || ["manifests/stable.json", "candidate/manifest.json", "last-known-good/manifest.json"].includes(key)) return key;
  if (key.startsWith("candidate/") && publicKeys.has(key.slice("candidate/".length))) return key;
  if (key.startsWith("last-known-good/") && publicKeys.has(key.slice("last-known-good/".length))) return key;
  const run = runPrefix.exec(key);
  if (run && (publicKeys.has(run[2]) || (key.startsWith("releases/") && run[2] === "manifest.json"))) return key;
  const previous = previousRelease.exec(key);
  return previous && publicKeys.has(previous[2]) ? key : null;
}

function failure(status, message, headers = {}) {
  return new Response(message, { status, headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", ...headers } });
}

export async function serveAggregatorArtifact(request, env) {
  const key = publicArtifactKey(new URL(request.url).pathname);
  if (key === null) return failure(404, "Not found");
  if (request.method !== "GET" && request.method !== "HEAD") return failure(405, "Method not allowed", { Allow: "GET, HEAD" });
  if (!env.AGGREGATOR_STORE) return failure(503, "Artifact store unavailable");
  let object;
  try {
    object = request.method === "HEAD" ? await env.AGGREGATOR_STORE.head(key) : await env.AGGREGATOR_STORE.get(key);
    if (object === null) return failure(404, "Not found");
    if (!object || !Number.isSafeInteger(object.size) || object.size < 0 || object.size > maxObjectBytes) {
      await object?.body?.cancel?.();
      return failure(503, "Artifact unavailable");
    }
    const headers = new Headers({
      "Content-Type": key.endsWith(".json") ? "application/json; charset=utf-8" : key.endsWith(".yaml") ? "application/yaml; charset=utf-8" : "text/plain; charset=utf-8",
      "Content-Length": String(object.size),
      "X-Content-Type-Options": "nosniff",
      "Cache-Control": key.startsWith("releases/") ? "public, max-age=31536000, immutable" : "no-store"
    });
    if (typeof object.httpEtag === "string" && /^"[a-fA-F0-9]{32}(?:-[0-9]+)?"$/.test(object.httpEtag)) headers.set("ETag", object.httpEtag);
    return new Response(request.method === "HEAD" ? null : object.body, { headers });
  } catch {
    try { await object?.body?.cancel?.(); } catch {}
    return failure(503, "Artifact unavailable");
  }
}

export function releaseMetadata(env) {
  if (typeof env.SBA_RELEASE_SHA !== "string" || env.SBA_RELEASE_SHA.length !== 40 || !/^[a-f0-9]{40}$/.test(env.SBA_RELEASE_SHA) ||
      typeof env.SBA_RELEASE_VERSION !== "string" || env.SBA_RELEASE_VERSION.trim() !== env.SBA_RELEASE_VERSION ||
      !/^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$/.test(env.SBA_RELEASE_VERSION)) {
    return failure(503, "Release metadata unavailable");
  }
  return Response.json({ schemaVersion: 1, applicationId: "easyproxy-cloud", sourceSha: env.SBA_RELEASE_SHA,
    applicationVersion: env.SBA_RELEASE_VERSION }, { headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" } });
}
