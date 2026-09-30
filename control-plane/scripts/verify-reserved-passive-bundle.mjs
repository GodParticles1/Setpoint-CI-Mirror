import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import vm from "node:vm";
import { verifyPassiveHandler } from "../test/helpers/reserved-passive-cases.mjs";

const path = new URL("../dist-dryrun/openai-observer/openai-consumer.js", import.meta.url);
const bytes = await readFile(path);
const logs = [];
let networkCalls = 0;
let registryConstructions = 0;
const context = vm.createContext({
  console: { log: (line) => logs.push(line) },
  fetch() { networkCalls += 1; throw new Error("Forbidden outbound request"); },
  TextEncoder, TextDecoder, URL,
});
const cloudflare = new vm.SyntheticModule(["DurableObject"], function () {
  this.setExport("DurableObject", class {
    constructor() { registryConstructions += 1; throw new Error("Passive entry must not instantiate Registry"); }
  });
}, { context });
const bundle = new vm.SourceTextModule(bytes.toString("utf8"), {
  context,
  identifier: path.href,
  importModuleDynamically() { throw new Error("Unexpected dynamic import"); },
});
await bundle.link((specifier) => {
  assert.equal(specifier, "cloudflare:workers", "No unexpected module capability");
  return cloudflare;
});
await bundle.evaluate();
assert.deepEqual(Object.keys(bundle.namespace).sort(), ["ModelInvocationRegistry", "default"]);
assert.equal(typeof bundle.namespace.ModelInvocationRegistry, "function");
const result = await verifyPassiveHandler(bundle.namespace.default, logs);
assert.equal(networkCalls, 0);
assert.equal(registryConstructions, 0);
console.log(JSON.stringify({
  acceptance: "RESERVED_PASSIVE_WRANGLER_BUNDLE", result: "PASS",
  sha256: createHash("sha256").update(bytes).digest("hex"), bytes: bytes.length,
  ...result, networkCalls, registryConstructions,
}));
