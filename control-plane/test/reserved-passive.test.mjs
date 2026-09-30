import test from "node:test";
import assert from "node:assert/strict";
import observer from "../src/reserved-passive-observer.js";
import { verifyPassiveHandler } from "./helpers/reserved-passive-cases.mjs";

test("RESERVED_PASSIVE validates, logs bounded metadata and ACKs with no capabilities", async (t) => {
  const logs = [];
  t.mock.method(console, "log", (line) => logs.push(line));
  const network = t.mock.method(globalThis, "fetch", () => assert.fail("No outbound requests allowed"));
  const result = await verifyPassiveHandler(observer, logs);
  assert.equal(network.mock.callCount(), 0);
  assert.equal(result.bindingReads, 0);
});
