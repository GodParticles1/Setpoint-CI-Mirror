import test from "node:test";
import assert from "node:assert/strict";
import {
  EXPECTED_REPOSITORY,
  buildDispatchEnvelope,
  evaluateEvent,
  getCoordinationLane,
  leaseAllowsDelivery,
  shouldSendToQueue,
  validateDispatchEnvelope,
} from "../src/async-policy.js";

function repo(extra = {}) {
  return { full_name: EXPECTED_REPOSITORY, default_branch: "main", ...extra };
}

test("writer final PR comment remains a high-priority wake signal", () => {
  const payload = {
    action: "created",
    repository: repo(),
    issue: { number: 64, pull_request: {} },
    comment: { body: "WRITER_FINAL: exact head ready", user: { login: "GodParticles1" } },
  };
  const result = evaluateEvent("issue_comment", payload, "");
  assert.equal(result.wake, true);
  assert.equal(result.signal, "WRITER_FINAL");
  assert.equal(result.prNumber, 64);
  assert.equal(getCoordinationLane("issue_comment", payload, result), "pr:64");
});

test("leader control comments are never queued", () => {
  const payload = {
    action: "created",
    repository: repo(),
    issue: { number: 69 },
    comment: { body: "LEADER_DECISION: continue", user: { login: "GodParticles1" } },
  };
  const result = evaluateEvent("issue_comment", payload, "");
  assert.equal(result.wake, false);
  assert.equal(result.reason, "LEADER_SELF_COMMENT_IGNORED");
  assert.equal(getCoordinationLane("issue_comment", payload, result), "");
});

test("issue-only writer final uses issue lane", () => {
  const payload = {
    action: "created",
    repository: repo(),
    issue: { number: 45 },
    comment: { body: "WRITER_BLOCKED: runner unavailable", user: { login: "writer" } },
  };
  const result = evaluateEvent("issue_comment", payload, "");
  assert.equal(result.wake, true);
  assert.equal(result.prNumber, null);
  assert.equal(result.issueNumber, 45);
  assert.equal(getCoordinationLane("issue_comment", payload, result), "issue:45");
});

test("same delivery owner can resume a pending enqueue on redelivery", () => {
  const owner = "delivery:3c982170-ada8-11f1-800b-c5e2b4842426";
  const lease = { acquired: false, owner };
  assert.equal(leaseAllowsDelivery(lease, owner), true);
  assert.equal(shouldSendToQueue({ dispatchState: "pending", lease, requestedOwner: owner }), true);
  assert.equal(shouldSendToQueue({ dispatchState: "queued", lease, requestedOwner: owner }), false);
});

test("a different lane owner cannot enqueue", () => {
  const owner = "delivery:3c982170-ada8-11f1-800b-c5e2b4842426";
  const lease = { acquired: false, owner: "delivery:e2cc63c0-ada9-11f1-9ed9-c56539497061" };
  assert.equal(shouldSendToQueue({ dispatchState: "pending", lease, requestedOwner: owner }), false);
});

test("dispatch envelope contains coordination metadata only", () => {
  const payload = {
    action: "synchronize",
    repository: repo(),
    pull_request: {
      number: 64,
      head: { sha: "a".repeat(40), ref: "writer" },
      base: { ref: "main" },
    },
    comment: { body: "THIS MUST NEVER BE STORED" },
  };
  const result = evaluateEvent("pull_request", payload, "");
  const envelope = buildDispatchEnvelope({
    delivery: "3c982170-ada8-11f1-800b-c5e2b4842426",
    event: "pull_request",
    payload,
    result,
    coordinationLane: "pr:64",
    leaseOwner: "delivery:3c982170-ada8-11f1-800b-c5e2b4842426",
    observedAt: "2026-09-14T07:00:00.000Z",
  });
  assert.equal(validateDispatchEnvelope(envelope), true);
  const serialized = JSON.stringify(envelope);
  assert.equal(serialized.includes("THIS MUST NEVER BE STORED"), false);
  assert.equal(serialized.includes("comment"), false);
  assert.equal(envelope.authority, "WAKE_SIGNAL_ONLY");
});

test("important workflow completion stays wakeable and non-completed is filtered", () => {
  const completed = {
    action: "completed",
    repository: repo(),
    workflow_run: { name: "Go CI", head_sha: "b".repeat(40), pull_requests: [] },
  };
  const inProgress = { ...completed, action: "in_progress" };
  assert.equal(evaluateEvent("workflow_run", completed, "").wake, true);
  assert.equal(evaluateEvent("workflow_run", inProgress, "").wake, false);
});
