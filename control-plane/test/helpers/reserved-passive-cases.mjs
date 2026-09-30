import assert from "node:assert/strict";

export function wakeEnvelope(overrides = {}) {
  const delivery = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
  return {
    schema_version: 1, delivery, repository: "GodParticles1/Setpoint",
    event: "pull_request", action: "synchronize", pr_number: 64, issue_number: null,
    head_sha: "9".repeat(40), signal: "PR_SYNCHRONIZE", priority: "high",
    reason: "PR_HEAD_CHANGED", coordination_lane: "pr:64",
    lease_owner: `delivery:${delivery}`, observed_at: "2026-09-30T00:00:00.000Z",
    authority: "WAKE_SIGNAL_ONLY", mode: "OBSERVE_ONLY", ...overrides,
  };
}

// Shared source + actual compiled-entry acceptance. Exceptions are not swallowed.
export async function verifyPassiveHandler(handler, logs) {
  let bindingReads = 0;
  const noCapabilities = new Proxy({}, {
    get(_target, key) {
      bindingReads += 1;
      throw new Error(`Forbidden capability access: ${String(key)}`);
    },
  });
  const valid = [];
  for (const event of ["push", "pull_request", "issue_comment", "pull_request_review", "pull_request_review_comment", "workflow_run"]) {
    for (const priority of ["none", "low", "medium", "high"]) {
      valid.push(wakeEnvelope({ event, priority, signal: "", pr_number: null }));
    }
  }
  valid.push(wakeEnvelope({ event: "e".repeat(120), signal: "s".repeat(120), delivery: "d".repeat(128) }));
  valid.push(wakeEnvelope({ event: "\u0000".repeat(120), signal: "\u0000".repeat(120), delivery: "d".repeat(128) }));
  const extraPayload = wakeEnvelope();
  for (const key of ["comment_body", "token", "secret", "source", "OPENAI_API_KEY", "GITHUB_READ_TOKEN"]) {
    Object.defineProperty(extraPayload, key, { enumerable: true, get() { throw new Error(`Read forbidden payload ${key}`); } });
  }
  valid.push(extraPayload);
  const malformed = [null, undefined, false, 1, "not an envelope", [], {},
    ...Object.keys(wakeEnvelope()).map((key) => { const e = wakeEnvelope(); delete e[key]; return e; }),
    ...[
      { schema_version: 2 }, { repository: "GodParticles1/Other" },
      { delivery: "bad" }, { event: "e".repeat(121) }, { signal: "s".repeat(121) },
      { observed_at: "invalid" }, { authority: "ACCEPTED_STATE" }, { mode: "ACTIVE" },
      { lease_owner: "invalid" }, { pr_number: -1 }, { issue_number: 0 },
    ].map((overrides) => wakeEnvelope(overrides)),
  ];
  let acknowledged = 0;
  const cases = [...valid.map((body) => ({ body, valid: true })), ...malformed.map((body) => ({ body, valid: false }))];
  const ackCounts = cases.map(() => 0);
  const messages = cases.map(({ body }, index) => ({
    body,
    ack() { acknowledged += 1; ackCounts[index] += 1; },
    retry() { assert.fail("Passive handling must not retry"); },
  }));
  await handler.queue({ messages }, noCapabilities, noCapabilities);
  assert.equal(bindingReads, 0);
  assert.equal(acknowledged, cases.length);
  assert.ok(ackCounts.every((count) => count === 1), "ACK each message exactly once");
  assert.equal(logs.length, cases.length);
  for (const [i, raw] of logs.entries()) {
    assert.equal(typeof raw, "string");
    assert.ok(Buffer.byteLength(raw, "utf8") <= 2048, "bounded metadata log");
    const record = JSON.parse(raw);
    const expected = {
      type: "agent_observer_reserved_passive", mode: "RESERVED_PASSIVE",
      result: cases[i].valid ? "PASSIVE_WAKE" : "INVALID_QUEUE_ENVELOPE",
      ...(cases[i].valid ? Object.fromEntries(["delivery", "repository", "event", "signal"].map((key) => [key, cases[i].body[key]])) : {}),
      openai_dispatch: false, github_live_read: false, github_mutation_count: 0,
    };
    assert.deepEqual(record, expected);
  }
  logs.length = 0;
  await handler.queue({ messages: [{ body: wakeEnvelope(), ack() { acknowledged += 1; } }] });
  assert.equal(acknowledged, cases.length + 1);
  assert.equal(logs.length, 1);
  logs.length = 0;
  await handler.queue({ messages: [] }, noCapabilities, noCapabilities);
  assert.equal(logs.length, 0);
  assert.equal(bindingReads, 0);
  return { valid: valid.length + 1, malformed: malformed.length, bindingReads, acknowledged };
}
