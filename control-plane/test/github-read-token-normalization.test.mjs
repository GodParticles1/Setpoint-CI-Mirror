import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

test("observer trims GITHUB_READ_TOKEN before constructing GitHub read client", async()=>{
  const source = await readFile(new URL("../src/openai-consumer.ts", import.meta.url), "utf8");
  assert.match(source, /token:\s*env\.GITHUB_READ_TOKEN\.trim\(\)/);
});

test("observer overrides bounded GitHub reads to manual redirect handling", async()=>{
  const source = await readFile(new URL("../src/openai-consumer.ts", import.meta.url), "utf8");
  assert.match(source, /fetchFn:\s*\(input, init\)\s*=>\s*fetch\(input,\s*\{\s*\.\.\.init,\s*redirect:\s*"manual"\s*\}\)/);
});
