import test from "node:test";
import assert from "node:assert/strict";
import { createEvidenceContractSession } from "../src/live-evidence-contract.js";
import { executeLiveTakeover } from "../src/live-takeover.js";
import { INVOCATION_STATES } from "../src/openai-core.js";

const MAIN = "1".repeat(40);
const PR_HEAD = "2".repeat(40);
const DELIVERY = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";

function envelope(overrides = {}) {
  return {
    schema_version: 1,
    delivery: DELIVERY,
    repository: "GodParticles1/Setpoint",
    event: "issue_comment",
    action: "created",
    pr_number: null,
    issue_number: 64,
    head_sha: "9".repeat(40),
    signal: "WRITER_FINAL",
    priority: "high",
    reason: "WRITER_FINAL_SIGNAL",
    coordination_lane: "issue:64",
    lease_owner: `delivery:${DELIVERY}`,
    observed_at: "2026-09-15T04:00:00.000Z",
    authority: "WAKE_SIGNAL_ONLY",
    mode: "OBSERVE_ONLY",
    ...overrides,
  };
}

function b64(text) { return Buffer.from(text, "utf8").toString("base64"); }
function page(path) { return Number(new URL(`https://api.github.com${path}`).searchParams.get("page") || "1"); }

class FakeClient {
  constructor({ issueBody = "writer final", linked = true, compareFiles = 1, totalCommits = 1 } = {}) {
    this.issueBody = issueBody;
    this.linked = linked;
    this.compareFiles = compareFiles;
    this.totalCommits = totalCommits;
    this.requests = [];
  }

  async request(path) {
    this.requests.push(path);
    if (path === "/repos/GodParticles1/Setpoint") return { default_branch: "main" };
    if (path === "/repos/GodParticles1/Setpoint/branches/main") return { commit: { sha: MAIN } };
    if (path.startsWith("/repos/GodParticles1/Setpoint/contents/")) return { encoding: "base64", content: b64("governance") };
    if (path === "/repos/GodParticles1/Setpoint/issues/45") return { number: 45, state: "open", body: "stable" };
    if (path.startsWith("/repos/GodParticles1/Setpoint/issues/45/comments")) return page(path) === 1 ? [] : [];
    if (path === "/repos/GodParticles1/Setpoint/issues/64") return { number: 64, state: "open", body: this.issueBody };
    if (path.startsWith("/repos/GodParticles1/Setpoint/issues/64/comments")) return [];
    if (path.startsWith("/repos/GodParticles1/Setpoint/issues/64/timeline")) {
      if (page(path) > 1 || !this.linked) return [];
      return [{ event: "cross-referenced", source: { issue: { number: 65, state: "open", title: "candidate", pull_request: {}, repository: { full_name: "GodParticles1/Setpoint" } } } }];
    }
    if (path === "/repos/GodParticles1/Setpoint/pulls/65") return { number: 65, state: "open", draft: true, head: { sha: PR_HEAD, ref: "candidate" }, base: { sha: MAIN, ref: "main" }, body: "candidate", changed_files: 1 };
    if (path.startsWith("/repos/GodParticles1/Setpoint/pulls/65/files")) return page(path) === 1 ? [{ filename: "app/x.go", status: "modified", additions: 1, deletions: 0, changes: 1, patch: "@@ safe" }] : [];
    if (path.includes(`/actions/runs?head_sha=${PR_HEAD}`)) return { workflow_runs: [] };
    if (path.includes(`/actions/runs?head_sha=${MAIN}`)) return { workflow_runs: [] };
    if (path.endsWith(`/commits/${PR_HEAD}/status`) || path.endsWith(`/commits/${MAIN}/status`)) return { state: "success" };
    if (path.includes("/statuses?")) return [];
    if (path.includes("/check-runs?")) return { check_runs: [] };
    if (path.includes("/compare/")) return {
      status: "ahead",
      ahead_by: 1,
      behind_by: 0,
      total_commits: this.totalCommits,
      files: Array.from({ length: this.compareFiles }, (_, i) => ({ filename: `app/f${i}.go`, status: "modified", additions: 1, deletions: 0, changes: 1 })),
    };
    throw new Error(`unexpected path ${path}`);
  }
}

async function collectPrEvidence(session) {
  await session.execute("setpoint_get_pr_files", { pr_number: 65 });
  await session.execute("setpoint_get_workflow_runs", { sha: PR_HEAD });
  await session.execute("setpoint_get_commit_status", { sha: PR_HEAD });
  await session.execute("setpoint_compare", { base_sha: MAIN, head_sha: PR_HEAD });
}

test("truncated affected issue body cannot satisfy issue evidence contract", async () => {
  const session = createEvidenceContractSession({ client: new FakeClient({ issueBody: "你".repeat(10000) }), envelope: envelope({ signal: "WRITER_BLOCKED" }) });
  await session.bootstrap();
  await session.execute("setpoint_get_issue_comments", { issue_number: 64 });
  assert.match(session.missingEvidence().join(","), /AFFECTED_ISSUE_BODY_COMPLETE/);
  assert.throws(() => session.verifyEvidenceComplete(), /INCOMPLETE_LIVE_EVIDENCE/);
});

test("WRITER_FINAL issue cannot complete from body and comments alone", async () => {
  const session = createEvidenceContractSession({ client: new FakeClient(), envelope: envelope() });
  await session.bootstrap();
  await session.execute("setpoint_get_issue_comments", { issue_number: 64 });
  assert.match(session.missingEvidence().join(","), /LINKED_PR_DISCOVERY/);
  assert.throws(() => session.verifyEvidenceComplete(), /INCOMPLETE_LIVE_EVIDENCE/);
});

test("WRITER_FINAL issue requires resolved reviewable PR and full PR evidence contract", async () => {
  const session = createEvidenceContractSession({ client: new FakeClient(), envelope: envelope() });
  await session.bootstrap();
  await session.execute("setpoint_get_issue_comments", { issue_number: 64 });
  const linked = await session.execute("setpoint_get_issue_linked_prs", { issue_number: 64 });
  assert.equal(linked.data.items[0].pr_number, 65);
  await session.execute("setpoint_get_pr", { pr_number: 65 });
  assert.match(session.missingEvidence().join(","), /PR_CHANGED_FILES_COMPLETE/);
  await collectPrEvidence(session);
  assert.deepEqual(session.missingEvidence(), []);
  assert.doesNotThrow(() => session.verifyEvidenceComplete());
});

test("WRITER_FINAL issue with no same-repository candidate fails closed", async () => {
  const session = createEvidenceContractSession({ client: new FakeClient({ linked: false }), envelope: envelope() });
  await session.bootstrap();
  await session.execute("setpoint_get_issue_comments", { issue_number: 64 });
  await session.execute("setpoint_get_issue_linked_prs", { issue_number: 64 });
  assert.match(session.missingEvidence().join(","), /REVIEWABLE_CANDIDATE_UNRESOLVED/);
});

test("default-branch push is a live-main lane and uses fresh live main evidence", async () => {
  const client = new FakeClient();
  const pushEnvelope = envelope({ event: "push", issue_number: null, signal: "", reason: "DEFAULT_BRANCH_PUSH", coordination_lane: "main", head_sha: "9".repeat(40) });
  const session = createEvidenceContractSession({ client, envelope: pushEnvelope });
  const boot = await session.bootstrap();
  assert.equal(boot.live_main.main_sha, MAIN);
  assert.equal(session.contract.laneKind, "live_main_push");
  assert.equal(session.state.authorizedShas.has(pushEnvelope.head_sha), false);
  await session.execute("setpoint_get_workflow_runs", { sha: MAIN });
  await session.execute("setpoint_get_commit_status", { sha: MAIN });
  assert.deepEqual(session.missingEvidence(), []);
});

test("large compare at 300-file boundary is fatal and never marked complete", async () => {
  const session = createEvidenceContractSession({ client: new FakeClient({ compareFiles: 300 }), envelope: envelope() });
  await session.bootstrap();
  await session.execute("setpoint_get_issue_comments", { issue_number: 64 });
  await session.execute("setpoint_get_issue_linked_prs", { issue_number: 64 });
  await session.execute("setpoint_get_pr", { pr_number: 65 });
  await session.execute("setpoint_get_pr_files", { pr_number: 65 });
  await session.execute("setpoint_get_workflow_runs", { sha: PR_HEAD });
  await session.execute("setpoint_get_commit_status", { sha: PR_HEAD });
  await assert.rejects(() => session.execute("setpoint_compare", { base_sha: MAIN, head_sha: PR_HEAD }), /COMPARE_EVIDENCE_INCOMPLETE/);
  assert.match(session.missingEvidence().join(","), /LIVE_MAIN_PR_COMPARE/);
});

test("compare at 250-commit boundary is conservatively incomplete", async () => {
  const session = createEvidenceContractSession({ client: new FakeClient({ totalCommits: 250 }), envelope: envelope() });
  await session.bootstrap();
  await session.execute("setpoint_get_issue_linked_prs", { issue_number: 64 });
  await session.execute("setpoint_get_pr", { pr_number: 65 });
  await assert.rejects(() => session.execute("setpoint_compare", { base_sha: MAIN, head_sha: PR_HEAD }), /COMPARE_EVIDENCE_INCOMPLETE/);
});

class FatalBootstrapClient extends FakeClient {
  async request(path) {
    if (path.startsWith("/repos/GodParticles1/Setpoint/contents/AGENTS.md")) return { encoding: "base64", content: b64("x".repeat(40000)) };
    return super.request(path);
  }
}

class Registry {
  constructor() { this.status = null; this.errorClass = ""; }
  async reserve() { this.status = INVOCATION_STATES.RESERVED; return { action: "DISPATCH", state: this.status, responseId: "", errorClass: "" }; }
  async markPreDispatchFailure({ errorClass }) { this.status = INVOCATION_STATES.FAILED_PRE_DISPATCH; this.errorClass = errorClass; }
  async markPreDispatchTerminal({ errorClass }) { this.status = INVOCATION_STATES.INVALID_OUTPUT; this.errorClass = errorClass; }
  async markDispatching() { this.status = INVOCATION_STATES.DISPATCHING; }
  async markCompleted() { this.status = INVOCATION_STATES.COMPLETED; }
  async markTerminal({ state, errorClass }) { this.status = state; this.errorClass = errorClass; }
  async markUncertain({ errorClass }) { this.status = INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH; this.errorClass = errorClass; }
}

test("fatal bootstrap evidence failure ACKs terminally with no model call and no retry", async () => {
  const registry = new Registry();
  let sends = 0;
  const transport = { model: "gpt-5.6", prepare() { return { url: "https://api.openai.com/v1/responses", body: "{}" }; }, async send() { sends += 1; throw new Error("must not send"); } };
  const outcome = await executeLiveTakeover({ envelope: envelope(), registry, transport, githubClient: new FatalBootstrapClient() });
  assert.equal(outcome.queueAction, "ack");
  assert.equal(outcome.failClosed, true);
  assert.equal(outcome.errorClass, "FILE_TOO_LARGE");
  assert.equal(registry.status, INVOCATION_STATES.INVALID_OUTPUT);
  assert.equal(sends, 0);
});
