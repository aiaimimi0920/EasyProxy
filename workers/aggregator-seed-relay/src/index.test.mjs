import assert from "node:assert/strict";
import test from "node:test";
import relay from "./index.js";

const UPSTREAM = "https://upstream.invalid/sub?token=private-upstream-token";
const CACHE = "https://cache.invalid/sub?token=private-cache-token";
const env = { ISSUE91_UPSTREAM_URL_B64: btoa(UPSTREAM), ISSUE91_CACHE_URL: CACHE };
const request = () => new Request("https://relay.invalid/issue91");
const payload = "proxies:\n  - name: example\n";

test("returns the upstream payload without fetching the cache", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url) => {
    calls.push(url);
    return new Response(payload);
  });
  const response = await relay.fetch(request(), env);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("x-easyproxy-relay-source"), "upstream");
  assert.equal(await response.text(), payload);
  assert.deepEqual(calls, [UPSTREAM]);
});

test("uses the configured cache after upstream failure", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url) => {
    calls.push(url);
    return new Response(payload, { status: url === UPSTREAM ? 503 : 200 });
  });
  const response = await relay.fetch(request(), env);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("x-easyproxy-relay-source"), "cache");
  assert.equal(await response.text(), payload);
  assert.deepEqual(calls, [UPSTREAM, CACHE]);
});

test("does not contact a maintainer cache when no cache is configured", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url) => {
    calls.push(url);
    return new Response("unavailable", { status: 503 });
  });
  const response = await relay.fetch(request(), { ISSUE91_UPSTREAM_URL_B64: btoa(UPSTREAM) });
  assert.equal(response.status, 502);
  assert.deepEqual(calls, [UPSTREAM]);
  assert.match(await response.text(), /upstream returned HTTP 503/);
});

for (const failure of ["http", "invalid-payload", "network"]) {
  test(`does not expose sensitive ${failure} failures`, async (t) => {
    t.mock.method(globalThis, "fetch", async (url) => {
      if (failure === "network") throw new Error(`failed to fetch ${url}`);
      return new Response(`private-response-body ${url}`, {
        status: failure === "http" ? 503 : 200,
      });
    });
    const response = await relay.fetch(request(), env);
    assert.equal(response.status, 502);
    assert.equal(response.headers.get("cache-control"), "no-store");
    const body = await response.text();
    assert.match(body, /upstream_fetch_failed/);
    assert.doesNotMatch(body, /private-|upstream\.invalid|cache\.invalid/);
  });
}

test("missing configuration fails without making a request", async (t) => {
  const fetch = t.mock.method(globalThis, "fetch", async () => {
    throw new Error("unexpected request");
  });
  assert.equal((await relay.fetch(request(), {})).status, 502);
  assert.equal(fetch.mock.callCount(), 0);
});
