import test from "node:test";
import assert from "node:assert/strict";
import {
  INVOCATION_STATES,
  RetryablePreDispatchError,
  createOpenAITransport,
  executeObserveOnly,
  validateLeaderDecision,
} from "../src/openai-core.js";

function envelope(delivery = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa") {
  return {
    schema_version: 1,
    delivery,
    repository: "GodParticles1/Setpoint",
    event: "issue_comment",
    action: "created",
    pr_number: 64,
    issue_number: 64,
    head_sha: "",
    signal: "WRITER_FINAL",
    priority: "high",
    reason: "WRITER_FINAL_SIGNAL",
    coordination_lane: "pr:64",
    lease_owner: `delivery:${delivery}`,
    observed_at: "2026-09-14T09:00:00.000Z",
    authority: "WAKE_SIGNAL_ONLY",
    mode: "OBSERVE_ONLY",
  };
}

function validDecision(overrides = {}) {
  return {
    LEADER_DECISION: "NO_ACTION",
    LANE_STATE: "ACTIVE",
    CANDIDATE_STATE: "REMOTE_REVIEWABLE",
    BLOCKER_CLASS: "NONE",
    ...overrides,
  };
}

function validResponse(id = "resp_test") {
  return {
    id,
    status: "completed",
    output: [{
      type: "message",
      content: [{
        type: "output_text",
        text: JSON.stringify(validDecision()),
      }],
    }],
    usage: { input_tokens: 10, output_tokens: 5, total_tokens: 15 },
  };
}

class MemoryRegistry {
  constructor() { this.records = new Map(); }
  async reserve({ delivery, model }) {
    const current = this.records.get(delivery);
    if (!current) {
      this.records.set(delivery, { model, status: INVOCATION_STATES.RESERVED, responseId: "", errorClass: "" });
      return { action: "DISPATCH", state: INVOCATION_STATES.RESERVED, responseId: "", errorClass: "" };
    }
    if (current.status === INVOCATION_STATES.COMPLETED) return { action: "REPLAY_COMPLETED", state: current.status, responseId: current.responseId, errorClass: current.errorClass };
    if (current.status === INVOCATION_STATES.FAILED_PRE_DISPATCH) { current.status = INVOCATION_STATES.RESERVED; current.errorClass = ""; return { action: "DISPATCH", state: current.status, responseId: "", errorClass: "" }; }
    if ([INVOCATION_STATES.RESERVED, INVOCATION_STATES.DISPATCHING].includes(current.status)) return { action: "IN_FLIGHT", state: current.status, responseId: current.responseId, errorClass: current.errorClass };
    return { action: "TERMINAL", state: current.status, responseId: current.responseId, errorClass: current.errorClass };
  }
  async markDispatching({ delivery }) { this.records.get(delivery).status = INVOCATION_STATES.DISPATCHING; }
  async markPreDispatchFailure({ delivery, errorClass }) { const r = this.records.get(delivery); r.status = INVOCATION_STATES.FAILED_PRE_DISPATCH; r.errorClass = errorClass; }
  async markCompleted({ delivery, responseId }) { const r = this.records.get(delivery); r.status = INVOCATION_STATES.COMPLETED; r.responseId = responseId; }
  async markTerminal({ delivery, state, responseId, errorClass }) { const r = this.records.get(delivery); r.status = state; r.responseId = responseId; r.errorClass = errorClass; }
  async markUncertain(input) { return this.markTerminal({ ...input, state: INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH }); }
}

function transport(send, prepare = () => ({})) { return { model: "gpt-5.6", prepare, send }; }

test("canonical leader decision fixture is accepted", () => {
  assert.equal(validateLeaderDecision(validDecision()), true);
});

test("non-canonical LANE_STATE=PENDING is rejected", () => {
  assert.equal(validateLeaderDecision(validDecision({ LANE_STATE: "PENDING" })), false);
});

test("non-canonical CANDIDATE_STATE=UNKNOWN is rejected", () => {
  assert.equal(validateLeaderDecision(validDecision({ CANDIDATE_STATE: "UNKNOWN" })), false);
});

test("duplicate queue replay does not repeat terminal model call", async () => {
  const registry = new MemoryRegistry(); let sends = 0;
  const t = transport(async () => { sends += 1; return { ok: true, status: 200, body: { id: "resp_refusal", status: "completed", output: [{ type: "message", content: [{ type: "refusal", refusal: "no" }] }], usage: {} } }; });
  assert.equal((await executeObserveOnly({ envelope: envelope(), registry, transport: t })).state, INVOCATION_STATES.REFUSED);
  assert.equal((await executeObserveOnly({ envelope: envelope(), registry, transport: t })).replay, true);
  assert.equal(sends, 1);
});

test("concurrent duplicate exclusion prevents second send", async () => {
  const registry = new MemoryRegistry(); let release; let sends = 0;
  const gate = new Promise((resolve) => { release = resolve; });
  const t = transport(async () => { sends += 1; await gate; return { ok: true, status: 200, body: validResponse("resp_concurrent") }; });
  const first = executeObserveOnly({ envelope: envelope(), registry, transport: t });
  while (sends === 0) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal((await executeObserveOnly({ envelope: envelope(), registry, transport: t })).queueAction, "retry");
  assert.equal(sends, 1); release(); await first; assert.equal(sends, 1);
});

test("completed replay is acked without another call", async () => {
  const registry = new MemoryRegistry(); let sends = 0;
  const t = transport(async () => { sends += 1; return { ok: true, status: 200, body: validResponse("resp_completed") }; });
  await executeObserveOnly({ envelope: envelope(), registry, transport: t });
  const replay = await executeObserveOnly({ envelope: envelope(), registry, transport: t });
  assert.equal(replay.queueAction, "ack"); assert.equal(replay.replay, true); assert.equal(sends, 1);
});

test("pre-dispatch failure is retryable", async () => {
  const registry = new MemoryRegistry(); let sends = 0;
  const failing = transport(async () => { sends += 1; }, () => { throw new RetryablePreDispatchError("OPENAI_API_KEY_MISSING"); });
  const first = await executeObserveOnly({ envelope: envelope(), registry, transport: failing });
  assert.equal(first.state, INVOCATION_STATES.FAILED_PRE_DISPATCH); assert.equal(first.queueAction, "retry"); assert.equal(sends, 0);
  const good = transport(async () => { sends += 1; return { ok: true, status: 200, body: validResponse("resp_retry") }; });
  assert.equal((await executeObserveOnly({ envelope: envelope(), registry, transport: good })).state, INVOCATION_STATES.COMPLETED); assert.equal(sends, 1);
});

test("uncertain after dispatch fails closed on redelivery", async () => {
  const registry = new MemoryRegistry(); let sends = 0;
  const t = transport(async () => { sends += 1; throw new Error("reset after write"); });
  assert.equal((await executeObserveOnly({ envelope: envelope(), registry, transport: t })).state, INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH);
  const replay = await executeObserveOnly({ envelope: envelope(), registry, transport: t });
  assert.equal(replay.queueAction, "ack"); assert.equal(replay.state, INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH); assert.equal(sends, 1);
});

test("invalid output refusal and incomplete are terminal", async () => {
  const cases = [
    [INVOCATION_STATES.INVALID_OUTPUT, { id: "resp_invalid", status: "completed", output: [{ type: "message", content: [{ type: "output_text", text: "{}" }] }], usage: {} }],
    [INVOCATION_STATES.REFUSED, { id: "resp_refused", status: "completed", output: [{ type: "message", content: [{ type: "refusal", refusal: "cannot" }] }], usage: {} }],
    [INVOCATION_STATES.INCOMPLETE, { id: "resp_incomplete", status: "incomplete", incomplete_details: { reason: "max_output_tokens" }, output: [], usage: {} }],
  ];
  for (const [index, [expected, response]] of cases.entries()) {
    const delivery = `bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb${index}`; const registry = new MemoryRegistry(); let sends = 0;
    const t = transport(async () => { sends += 1; return { ok: true, status: 200, body: response }; });
    assert.equal((await executeObserveOnly({ envelope: envelope(delivery), registry, transport: t })).state, expected);
    assert.equal((await executeObserveOnly({ envelope: envelope(delivery), registry, transport: t })).state, expected); assert.equal(sends, 1);
  }
});

test("OPENAI_API_KEY is not placed in prompt or registry", async () => {
  const registry = new MemoryRegistry(); const secret = "sk-test-secret-do-not-persist-123456789"; let captured;
  const t = createOpenAITransport({ apiKey: secret, model: "gpt-5.6", fetchFn: async (url, init) => { captured = { url, init }; return { ok: true, status: 200, async json() { return validResponse("resp_secret"); } }; } });
  assert.equal((await executeObserveOnly({ envelope: envelope(), registry, transport: t })).state, INVOCATION_STATES.COMPLETED);
  assert.equal(captured.init.headers.authorization, `Bearer ${secret}`); assert.equal(captured.init.body.includes(secret), false); assert.equal(JSON.stringify([...registry.records.values()]).includes(secret), false);
});


test("429 rate-limit error metadata is parsed and sanitized", async () => {
  const secret = "sk-test-secret-do-not-return-123456789";
  const requestBodyMarker = "request-body-marker-never-return";
  const t = createOpenAITransport({
    apiKey: secret,
    model: "gpt-5.6",
    fetchFn: async (_url, init) => {
      assert.equal(init.headers.authorization, `Bearer ${secret}`);
      assert.equal(init.body.includes(requestBodyMarker), false);
      return new Response(JSON.stringify({
        error: {
          type: "rate_limit_error",
          code: "rate_limit_exceeded",
          message: "Too many requests\nplease retry later",
          internal: "raw-body-secret-marker",
        },
      }), {
        status: 429,
        headers: { "x-request-id": "req_rate_123" },
      });
    },
  });
  const prepared = t.prepare(envelope());
  const remote = await t.send(prepared);
  assert.equal(remote.ok, false);
  assert.equal(remote.status, 429);
  assert.deepEqual(remote.error, {
    type: "rate_limit_error",
    code: "rate_limit_exceeded",
    message: "Too many requests please retry later",
    requestId: "req_rate_123",
  });
  const serialized = JSON.stringify(remote);
  assert.equal(serialized.includes(secret), false);
  assert.equal(serialized.includes("raw-body-secret-marker"), false);
  assert.equal(serialized.includes(prepared.body), false);
});

test("429 billing error metadata is sanitized and x-request-id is captured", async () => {
  const registry = new MemoryRegistry();
  const t = createOpenAITransport({
    apiKey: "sk-test-billing-key-123456789",
    model: "gpt-5.6",
    fetchFn: async () => new Response(JSON.stringify({
      error: {
        type: "insufficient_quota",
        code: "credit_balance_exhausted",
        message: "Credit balance exhausted.\r\nAdd credits to continue.",
      },
    }), {
      status: 429,
      headers: { "x-request-id": "req_billing_456" },
    }),
  });
  const out = await executeObserveOnly({ envelope: envelope("cccccccc-cccc-cccc-cccc-cccccccccccc"), registry, transport: t });
  assert.equal(out.state, INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH);
  assert.equal(out.errorClass, "OPENAI_HTTP_429");
  assert.equal(out.openaiHttpStatus, 429);
  assert.equal(out.openaiErrorType, "insufficient_quota");
  assert.equal(out.openaiErrorCode, "credit_balance_exhausted");
  assert.equal(out.openaiErrorMessage, "Credit balance exhausted. Add credits to continue.");
  assert.equal(out.openaiRequestId, "req_billing_456");
});

test("malformed non-JSON OpenAI error body fails closed without raw-body leakage", async () => {
  const raw = "<html>proxy raw body secret-marker</html>";
  const t = createOpenAITransport({
    apiKey: "sk-test-malformed-key-123456789",
    model: "gpt-5.6",
    fetchFn: async () => new Response(raw, {
      status: 429,
      headers: { "x-request-id": "req_malformed_789" },
    }),
  });
  const remote = await t.send(t.prepare(envelope("dddddddd-dddd-dddd-dddd-dddddddddddd")));
  assert.equal(remote.ok, false);
  assert.deepEqual(remote.error, {
    type: "",
    code: "",
    message: "",
    requestId: "req_malformed_789",
  });
  assert.equal(JSON.stringify(remote).includes(raw), false);
});

test("OpenAI error metadata is bounded by UTF-8 bytes", async () => {
  const t = createOpenAITransport({
    apiKey: "sk-test-bounds-key-123456789",
    model: "gpt-5.6",
    fetchFn: async () => new Response(JSON.stringify({
      error: {
        type: "T".repeat(300),
        code: "C".repeat(300),
        message: "你".repeat(300),
      },
    }), {
      status: 429,
      headers: { "x-request-id": "R".repeat(300) },
    }),
  });
  const remote = await t.send(t.prepare(envelope("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")));
  assert.ok(Buffer.byteLength(remote.error.type, "utf8") <= 128);
  assert.ok(Buffer.byteLength(remote.error.code, "utf8") <= 128);
  assert.ok(Buffer.byteLength(remote.error.message, "utf8") <= 256);
  assert.ok(Buffer.byteLength(remote.error.requestId, "utf8") <= 192);
  assert.equal(remote.error.message.includes("\n"), false);
  assert.equal(remote.error.message.includes("\r"), false);
});
