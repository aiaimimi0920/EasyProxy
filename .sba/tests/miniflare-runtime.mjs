// 两个实际锁定的测试运行时使用不同的配置 API；未知版本拒绝，而不是静默降级。
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

export function runtimeOptions(version, runtime, options, persistence) {
  assert.ok(path.isAbsolute(persistence));
  if (version === "4.20260520.0") {
    assert.equal(runtime.convertV4MiniflareOptions, undefined);
    return { ...options, d1Persist: path.join(persistence, "d1"), r2Persist: path.join(persistence, "r2") };
  }
  assert.equal(version, "5.20261001.0-alpha", "Unsupported explicit Miniflare runtime");
  assert.equal(typeof runtime.convertV4MiniflareOptions, "function");
  return runtime.convertV4MiniflareOptions({ ...options, resourcePersistencePath: persistence, telemetry: { enabled: false } });
}

export async function createRuntime(entrypoint, options, persistence) {
  assert.ok(path.isAbsolute(entrypoint));
  const metadata = JSON.parse(await readFile(path.resolve(path.dirname(entrypoint), "../../package.json"), "utf8"));
  assert.equal(metadata.name, "miniflare");
  const runtime = await import(pathToFileURL(entrypoint).href);
  return new runtime.Miniflare(runtimeOptions(metadata.version, runtime, options, persistence));
}
