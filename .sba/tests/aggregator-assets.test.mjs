import assert from "node:assert/strict";
import test from "node:test";
import { isAggregatorRoute, publicArtifactKey, releaseMetadata, serveAggregatorArtifact } from "../aggregator-assets.mjs";

test("only documented publication keys and run-scoped aliases are public", () => {
  for (const key of ["subs/effective.txt", "subs/clash.yaml", "internal/crawledsubs.json", "manifests/stable.json", "candidate/manifest.json",
    "candidate/subs/v2ray.txt", "candidate/123-1-abc/subs/mixed.txt", "releases/123-1-abc/subs/singbox.json",
    "releases/123-1-abc/manifest.json", "last-known-good/manifest.json", "last-known-good/subs/effective.json",
    "last-known-good/releases/123-1-abc/subs/clash.yaml"]) {
    assert.equal(publicArtifactKey("/aggregator/" + key), key);
  }
});

test("control state, backups, private crawler data and arbitrary keys never reach R2", async () => {
  let reads = 0;
  const env = { AGGREGATOR_STORE: { get() { reads++; throw new Error("must not read"); } } };
  for (const key of ["_sba/control-v1.json", "_sba/backups/archive.age", "internal/crawledproxies.txt", "private.txt",
    "releases/run/_sba/control-v1.json", "candidate/run/internal/crawledproxies.txt", "last-known-good/releases/run/private.txt",
    "candidate//subs/effective.txt", "candidate/../subs/effective.txt", "candidate/./subs/effective.txt", "subs/effective.txt/",
    "subs%2feffective.txt", "%5fsba/control-v1.json", "subs/effective.txt%00", "candidate/" + "a".repeat(129) + "/subs/mixed.txt"]) {
    assert.equal(publicArtifactKey("/aggregator/" + key), null, key);
    if (!key.includes("/../") && !key.includes("/./")) {
      assert.equal((await serveAggregatorArtifact(new Request("https://edge.invalid/aggregator/" + key), env)).status, 404, key);
    }
  }
  assert.equal(reads, 0);
  assert.equal(publicArtifactKey("/aggregator/releases/run/subs/mixed.txt\n"), null);
  assert.equal(publicArtifactKey("/aggregator/subs/mixed.txt\r"), null);
});

test("route matcher cannot capture unrelated ECH endpoints", () => {
  for (const path of ["/", "/connect", "/aggregators", "/.well-known/easyproxy-release.json"]) assert.equal(isAggregatorRoute(path), false);
  for (const path of ["/aggregator", "/aggregator/", "/aggregator/subs/mixed.txt"]) assert.equal(isAggregatorRoute(path), true);
});

test("GET streams exact bytes; HEAD reads metadata without opening object body", async () => {
  const methods = [];
  const env = { AGGREGATOR_STORE: {
    async get(key) { methods.push(["get", key]); return { size: 4, httpEtag: '"' + "a".repeat(32) + '"', body: new Blob(["data"]).stream() }; },
    async head(key) { methods.push(["head", key]); return { size: 4, httpEtag: '"' + "a".repeat(32) + '"' }; }
  } };
  const get = await serveAggregatorArtifact(new Request("https://edge.invalid/aggregator/subs/effective.txt?easyproxy_release=run"), env);
  assert.equal(get.status, 200);
  assert.equal(await get.text(), "data");
  assert.equal(get.headers.get("Content-Length"), "4");
  assert.equal(get.headers.get("Cache-Control"), "no-store");
  assert.equal(get.headers.get("Content-Type"), "text/plain; charset=utf-8");
  const head = await serveAggregatorArtifact(new Request("https://edge.invalid/aggregator/subs/effective.txt", { method: "HEAD" }), env);
  assert.equal(await head.text(), "");
  assert.deepEqual(methods, [["get", "subs/effective.txt"], ["head", "subs/effective.txt"]]);
});

test("only immutable release objects are cacheable and object metadata cannot inject headers", async () => {
  const env = { AGGREGATOR_STORE: { async get() { return { size: 2, body: "{}", httpEtag: "bad\r\nSet-Cookie: secret",
    writeHttpMetadata() { throw new Error("untrusted metadata must not be copied"); } }; } } };
  const response = await serveAggregatorArtifact(new Request("https://edge.invalid/aggregator/releases/run/manifest.json"), env);
  assert.equal(response.headers.get("Cache-Control"), "public, max-age=31536000, immutable");
  assert.equal(response.headers.get("ETag"), null);
  assert.equal(response.headers.get("Set-Cookie"), null);
  assert.equal(response.headers.get("Content-Type"), "application/json; charset=utf-8");
});

test("write methods are rejected without any provider call", async () => {
  for (const method of ["POST", "PUT", "PATCH", "DELETE", "OPTIONS"]) {
    const response = await serveAggregatorArtifact(new Request("https://edge.invalid/aggregator/subs/mixed.txt", { method }), {});
    assert.equal(response.status, 405);
    assert.equal(response.headers.get("Allow"), "GET, HEAD");
  }
});

test("provider errors, missing binding, oversized bodies and missing artifacts remain explicit safe failures", async () => {
  const request = new Request("https://edge.invalid/aggregator/subs/mixed.txt");
  assert.equal((await serveAggregatorArtifact(request, {})).status, 503);
  let cancelled = false;
  const response = await serveAggregatorArtifact(request, { AGGREGATOR_STORE: { async get() {
    return { size: 64 * 1024 * 1024 + 1, body: { async cancel() { cancelled = true; } } };
  } } });
  assert.equal(response.status, 503);
  assert.equal(cancelled, true);
  const missing = await serveAggregatorArtifact(request, { AGGREGATOR_STORE: { async get() { return null; } } });
  assert.equal(missing.status, 404);
  const failed = await serveAggregatorArtifact(request, { AGGREGATOR_STORE: { async get() { throw new Error("private-secret-value"); } } });
  assert.equal(failed.status, 503);
  assert.equal((await failed.text()).includes("private-secret-value"), false);
});

test("independent release metadata is exact and never includes environment secrets", async () => {
  const env = { SBA_RELEASE_SHA: "a".repeat(40), SBA_RELEASE_VERSION: "1.2.3", ECH_TOKEN: "private-token" };
  const response = releaseMetadata(env);
  assert.deepEqual(await response.json(), { schemaVersion: 1, applicationId: "easyproxy-cloud", sourceSha: "a".repeat(40), applicationVersion: "1.2.3" });
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  for (const overrides of [{ SBA_RELEASE_SHA: "main" }, { SBA_RELEASE_SHA: "a".repeat(40) + "\n" },
    { SBA_RELEASE_VERSION: "01.2.3" }, { SBA_RELEASE_VERSION: "1.2.3-beta" }, { SBA_RELEASE_VERSION: "1.2.3\n" }]) {
    assert.equal(releaseMetadata({ ...env, ...overrides }).status, 503);
  }
});
