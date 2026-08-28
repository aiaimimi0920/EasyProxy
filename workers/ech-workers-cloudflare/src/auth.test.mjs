import assert from "node:assert/strict";
import test from "node:test";

import { selectAcceptedToken } from "./auth.mjs";

const NOW = Date.parse("2026-08-29T00:00:00Z");

test("requires a configured current token", () => {
  assert.deepEqual(selectAcceptedToken("token", {}, NOW), { configured: false, token: "" });
});

test("accepts only the current token during an ordinary deployment", () => {
  const env = { ECH_TOKEN: "current" };
  assert.equal(selectAcceptedToken("current", env, NOW).token, "current");
  assert.equal(selectAcceptedToken("wrong", env, NOW).token, "");
});

test("selects a valid token from a WebSocket subprotocol offer list", () => {
  const env = { ECH_TOKEN: "current" };
  assert.equal(selectAcceptedToken("other, current", env, NOW).token, "current");
});

test("accepts an unexpired previous token during rotation overlap", () => {
  const env = {
    ECH_TOKEN: "current",
    ECH_TOKEN_PREVIOUS: "previous",
    ECH_TOKEN_PREVIOUS_EXPIRES_AT: "2026-08-29T00:05:00Z"
  };
  assert.equal(selectAcceptedToken("previous", env, NOW).token, "previous");
});

test("rejects a previous token without a valid future expiry", () => {
  for (const expiry of ["", "invalid", "2026-08-28T23:59:59Z", String(NOW / 1000)]) {
    const result = selectAcceptedToken(
      "previous",
      { ECH_TOKEN: "current", ECH_TOKEN_PREVIOUS: "previous", ECH_TOKEN_PREVIOUS_EXPIRES_AT: expiry },
      NOW
    );
    assert.equal(result.token, "");
  }
});
