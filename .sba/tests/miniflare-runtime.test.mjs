import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { runtimeOptions } from "./miniflare-runtime.mjs";

const persistence = path.resolve("fixture-persistence");
const options = { modules: true, script: "fixture", cf: false };

test("locked Miniflare v4 receives its real D1/R2 persistence options", () => {
  assert.deepEqual(runtimeOptions("4.20260520.0", {}, options, persistence), {
    ...options, d1Persist: path.join(persistence, "d1"), r2Persist: path.join(persistence, "r2"),
  });
});

test("locked Miniflare v5 uses the explicit compatibility converter", () => {
  let received;
  const runtime = { convertV4MiniflareOptions: value => { received = value; return { converted: true }; } };
  assert.deepEqual(runtimeOptions("5.20261001.0-alpha", runtime, options, persistence), { converted: true });
  assert.deepEqual(received, { ...options, resourcePersistencePath: persistence, telemetry: { enabled: false } });
});

test("unknown runtime or missing v5 converter cannot silently select another API", () => {
  for (const version of ["3.0.0", "4.99999999.0", "5.0.0", "6.0.0"]) {
    assert.throws(() => runtimeOptions(version, {}, options, persistence), /Unsupported explicit Miniflare runtime/);
  }
  assert.throws(() => runtimeOptions("5.20261001.0-alpha", {}, options, persistence));
});
