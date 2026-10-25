// 显式提供已有 Miniflare 路径与临时目录，不为产品新增运行时依赖。
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { once } from "node:events";
import { mkdir, mkdtemp, readFile } from "node:fs/promises";
import http from "node:http";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { createRuntime } from "./miniflare-runtime.mjs";

const runtime = process.env.EASYPROXY_TEST_MINIFLARE;
const artifacts = process.env.EASYPROXY_TEST_ARTIFACT_ROOT;

for (const purpose of ["production", "preview"]) test(`SBA edge composition in real workerd (${purpose})`, { skip: !runtime || !artifacts, timeout: 30000 }, async t => {
  assert.ok(path.isAbsolute(runtime) && path.isAbsolute(artifacts));
  await mkdir(artifacts, { recursive: true });
  const directory = await mkdtemp(path.join(artifacts, "edge-workerd-"));
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
  const { build } = createRequire(runtime)("esbuild");
  const preparedBundle = process.env.EASYPROXY_TEST_EDGE_BUNDLE;
  if (preparedBundle) assert.ok(path.isAbsolute(preparedBundle));
  const bundle = preparedBundle ? await readFile(preparedBundle, "utf8") :
    (await build({ entryPoints: [path.join(root, ".sba/edge.mjs")], bundle: true, write: false,
      format: "esm", platform: "browser", external: ["cloudflare:sockets"] })).outputFiles[0].text;
  const mf = await createRuntime(runtime, {
    modules: true, script: bundle,
    compatibilityDate: "2025-01-15", r2Buckets: ["AGGREGATOR_STORE"],
    cf: false, logRequests: false, host: "127.0.0.1", port: 0,
    bindings: { ECH_TOKEN: "fixture-ech-token", SBA_RELEASE_SHA: "a".repeat(40), SBA_RELEASE_VERSION: "1.2.3", SBA_PURPOSE: purpose }
  }, directory);
  t.after(() => mf.dispose());
  const address = await mf.ready;
  const bucket = await mf.getR2Bucket("AGGREGATOR_STORE");
  await bucket.put("subs/effective.txt", "fixture-effective-body");
  await bucket.put("releases/run/manifest.json", '{"fixture":true}');
  await bucket.put("_sba/control-v1.json", "must-not-be-public");
  await bucket.put("_sba/backups/private.age", "encrypted-fixture");
  await bucket.put("_sba/operations/private/ech-publication.json", "private-stage-marker");
  await bucket.put("internal/crawledproxies.txt", "private-crawler-fixture");

  await t.test("public artifact reads private R2 through existing ECH Worker composition", async () => {
    const response = await mf.dispatchFetch("https://edge.invalid/aggregator/subs/effective.txt");
    assert.equal(response.status, 200);
    assert.equal(await response.text(), "fixture-effective-body");
    assert.equal(response.headers.get("Cache-Control"), "no-store");
    // workerd 对普通 ReadableStream 使用流式传输，不承诺 GET 的 Content-Length。
    assert.match(response.headers.get("ETag"), /^"[a-f0-9]{32}"$/);
    const head = await mf.dispatchFetch("https://edge.invalid/aggregator/subs/effective.txt", { method: "HEAD" });
    assert.equal(head.status, 200);
    assert.equal(await head.text(), "");
    assert.equal(head.headers.get("Content-Length"), "22");
  });

  await t.test("private and encoded object paths cannot disclose the control record or backups", async () => {
    for (const suffix of ["_sba/control-v1.json", "_sba/backups/private.age", "_sba/operations/private/ech-publication.json", "internal/crawledproxies.txt",
      "releases/run/_sba/control-v1.json", "%5fsba/control-v1.json", "subs%2feffective.txt"]) {
      const response = await mf.dispatchFetch("https://edge.invalid/aggregator/" + suffix);
      assert.equal(response.status, 404, suffix);
      assert.equal(await response.text(), "Not found");
    }
  });

  await t.test("release metadata reports exact public version without secret values", async () => {
    const response = await mf.dispatchFetch("https://edge.invalid/.well-known/easyproxy-release.json");
    assert.deepEqual(await response.json(), { schemaVersion: 1, applicationId: "easyproxy-cloud", sourceSha: "a".repeat(40), applicationVersion: "1.2.3" });
    const head = await mf.dispatchFetch("https://edge.invalid/.well-known/easyproxy-release.json", { method: "HEAD" });
    assert.equal(head.status, 200);
    assert.equal(await head.text(), "");
  });

  if (purpose === "production") await t.test("original ECH root and unauthenticated WebSocket protection stay in place", async () => {
    const rootResponse = await mf.dispatchFetch("https://edge.invalid/");
    assert.equal(rootResponse.status, 200);
    assert.equal(await rootResponse.text(), "WebSocket Proxy Server");
    const rejected = await new Promise((resolve, reject) => {
      const request = http.get(new URL("/connect", address), { headers: { Upgrade: "websocket", Connection: "Upgrade",
        "Sec-WebSocket-Key": randomBytes(16).toString("base64"), "Sec-WebSocket-Version": "13" } }, response => {
        const chunks = [];
        response.on("data", chunk => chunks.push(chunk));
        response.on("end", () => resolve({ status: response.statusCode, body: Buffer.concat(chunks).toString() }));
        response.on("error", reject);
      });
      request.setTimeout(5000, () => request.destroy(new Error("fixture timeout")));
      request.on("error", reject);
    });
    assert.equal(rejected.status, 401);
    assert.equal(rejected.body, "Unauthorized");
  });

  if (purpose === "production") await t.test("valid ECH token still upgrades and closes without performing a network connection", async () => {
    const target = new URL("/connect", address);
    target.protocol = "ws:";
    const socket = new WebSocket(target, "fixture-ech-token");
    await once(socket, "open", { signal: AbortSignal.timeout(5000) });
    assert.equal(socket.protocol, "fixture-ech-token");
    const closed = once(socket, "close", { signal: AbortSignal.timeout(5000) });
    socket.close(1000, "fixture complete");
    await closed;
  });

  if (purpose === "preview") await t.test("preview denies proxy routes including authenticated WebSocket upgrade", async () => {
    for (const pathname of ["/", "/connect", "/unknown"]) {
      const response = await mf.dispatchFetch("https://edge.invalid" + pathname, { headers: {
        Upgrade: "websocket", "Sec-WebSocket-Protocol": "fixture-ech-token" } });
      assert.equal(response.status, 403);
      assert.equal(response.headers.get("Cache-Control"), "no-store");
      assert.equal(await response.text(), "Preview isolated");
    }
  });

  await t.test("write requests are rejected and leave private R2 unchanged", async () => {
    const response = await mf.dispatchFetch("https://edge.invalid/aggregator/subs/effective.txt", { method: "PUT", body: "overwrite" });
    assert.equal(response.status, 405);
    assert.equal(await (await bucket.get("subs/effective.txt")).text(), "fixture-effective-body");
    assert.equal(await (await bucket.get("_sba/control-v1.json")).text(), "must-not-be-public");
  });
});
