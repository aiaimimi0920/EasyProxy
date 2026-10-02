import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const source = readFileSync(new URL("./index.js", import.meta.url), "utf8")
  .replace(/^import .*;\r?\n/gm, "")
  .replace("export default", "const worker =");
const tick = () => new Promise((resolve) => setImmediate(resolve));

function session(connect) {
  const handlers = {};
  const ws = {
    readyState: 1,
    sent: [],
    addEventListener(name, handler) { handlers[name] = handler; },
    send(value) { this.sent.push(value); },
    close() { this.readyState = 3; }
  };
  const context = vm.createContext({ connect, TextEncoder, Uint8Array, ArrayBuffer, console, atob });
  vm.runInContext(source, context);
  context.handleSession(ws);
  return { ws, handlers };
}

test("failed TCP open handles closed and close promise rejections", async () => {
  const failure = new Error("Network connection lost.");
  const { ws, handlers } = session(() => ({
    opened: Promise.reject(failure),
    closed: Promise.reject(failure),
    close: () => Promise.reject(failure)
  }));
  await handlers.message({ data: "CONNECT:example.com:443||" });
  await tick();
  assert.equal(ws.readyState, 3);
  assert.equal(ws.sent[0], "ERROR:Network connection lost.");
});

test("client closing during TCP open cannot acquire streams or send CONNECTED", async () => {
  let opened;
  let acquisitions = 0;
  const { ws, handlers } = session(() => ({
    opened: new Promise((resolve) => { opened = resolve; }),
    closed: Promise.resolve(),
    close: () => Promise.reject(new Error("already closed")),
    writable: { getWriter() { acquisitions++; } },
    readable: { getReader() { acquisitions++; } }
  }));
  const pending = handlers.message({ data: "CONNECT:example.com:443||" });
  handlers.close();
  opened();
  await pending;
  await tick();
  assert.equal(acquisitions, 0);
  assert.equal(ws.sent.length, 0);
});

test("successful tunnel forwards bytes and closes once on remote EOF", async () => {
  let reads = 0;
  let closes = 0;
  const bytes = new Uint8Array([1, 2, 3]);
  const { ws, handlers } = session(() => ({
    opened: Promise.resolve(),
    closed: Promise.resolve(),
    close: async () => { closes++; },
    writable: { getWriter: () => ({ releaseLock() {}, write: async () => {} }) },
    readable: { getReader: () => ({ releaseLock() {}, read: async () => ++reads === 1
      ? { done: false, value: bytes } : { done: true } }) }
  }));
  await handlers.message({ data: "CONNECT:example.com:443||" });
  await tick();
  handlers.close();
  assert.deepEqual(ws.sent, ["CONNECTED", bytes, "CLOSE"]);
  assert.equal(closes, 1);
});
