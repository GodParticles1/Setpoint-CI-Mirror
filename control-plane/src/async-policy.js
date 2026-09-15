export const EXPECTED_REPOSITORY = "GodParticles1/Setpoint";
export const DEFAULT_BRANCH = "main";
export const WEBHOOK_PATH = "/github/setpoint";
export const MAX_BODY_BYTES = 1024 * 1024;
export const COORDINATOR_LEASE_TTL_SECONDS = 30;
export const DISPATCH_SCHEMA_VERSION = 1;

export const SUPPORTED_EVENTS = new Set([
  "ping",
  "push",
  "pull_request",
  "issue_comment",
  "pull_request_review",
  "pull_request_review_comment",
  "workflow_run",
]);

const IMPORTANT_WORKFLOWS = new Set(["Go CI", "Go Race Gate"]);
const LEADER_PREFIXES = ["LEADER_DECISION:", "LEADER_CONTROL:"];
const WRITER_PREFIXES = [
  "WRITER_FINAL:",
  "WRITER_BLOCKED:",
  "WRITER_PROGRESS:",
];

function normalizeText(value) {
  return typeof value === "string" ? value.trimStart() : "";
}

function startsWithPrefix(body, prefixes) {
  const normalized = normalizeText(body).toUpperCase();
  return prefixes.some((prefix) => normalized.startsWith(prefix));
}

function writerSignal(body) {
  const normalized = normalizeText(body).toUpperCase();
  for (const prefix of WRITER_PREFIXES) {
    if (normalized.startsWith(prefix)) {
      return prefix.slice(0, -1);
    }
  }
  return "";
}

function isLeaderControlBody(body) {
  return startsWithPrefix(body, LEADER_PREFIXES);
}

export function getActorLogin(payload) {
  return (
    payload?.sender?.login ||
    payload?.comment?.user?.login ||
    payload?.review?.user?.login ||
    ""
  );
}

function isConfiguredLeaderActor(payload, leaderGithubLogin) {
  const configured = String(leaderGithubLogin || "").trim().toLowerCase();
  if (!configured) {
    return false;
  }
  return getActorLogin(payload).toLowerCase() === configured;
}

export function getPrNumber(event, payload) {
  if (
    event === "pull_request" ||
    event === "pull_request_review" ||
    event === "pull_request_review_comment"
  ) {
    return payload?.pull_request?.number || payload?.number || null;
  }
  if (event === "issue_comment" && payload?.issue?.pull_request) {
    return payload?.issue?.number || null;
  }
  if (event === "workflow_run") {
    const prs = payload?.workflow_run?.pull_requests;
    if (Array.isArray(prs) && prs.length > 0) {
      return prs[0]?.number || null;
    }
  }
  return null;
}

export function getIssueNumber(event, payload) {
  return event === "issue_comment" ? payload?.issue?.number || null : null;
}

export function getCandidateHeadSha(event, payload) {
  if (event === "push") {
    return payload?.after || "";
  }
  if (
    event === "pull_request" ||
    event === "pull_request_review" ||
    event === "pull_request_review_comment"
  ) {
    return payload?.pull_request?.head?.sha || "";
  }
  if (event === "workflow_run") {
    return payload?.workflow_run?.head_sha || "";
  }
  return "";
}

function decision({
  wake = false,
  accepted = true,
  reason,
  priority = "none",
  signal = "",
  ref = "",
  headSha = "",
  prNumber = null,
  issueNumber = null,
}) {
  return {
    wake,
    accepted,
    reason,
    priority,
    signal,
    ref,
    headSha,
    prNumber,
    issueNumber,
  };
}

export function evaluateEvent(event, payload, leaderGithubLogin = "") {
  const action = payload?.action || "";
  const prNumber = getPrNumber(event, payload);
  const issueNumber = getIssueNumber(event, payload);

  if (event === "ping") {
    return decision({ wake: false, reason: "PING" });
  }

  if (event === "push") {
    const ref = payload?.ref || "";
    const expectedRef = `refs/heads/${payload?.repository?.default_branch || DEFAULT_BRANCH}`;
    if (ref !== expectedRef) {
      return decision({ wake: false, reason: "NON_DEFAULT_BRANCH_PUSH", ref });
    }
    return decision({
      wake: true,
      reason: "DEFAULT_BRANCH_PUSH",
      priority: "high",
      ref,
      headSha: payload?.after || "",
    });
  }

  if (event === "pull_request") {
    const pr = payload?.pull_request;
    const headSha = pr?.head?.sha || "";
    const ref = `${pr?.head?.ref || ""} -> ${pr?.base?.ref || ""}`;
    const base = { headSha, ref, prNumber };
    if (action === "synchronize") {
      return decision({ wake: true, reason: "PR_HEAD_CHANGED", priority: "high", ...base });
    }
    if (action === "ready_for_review") {
      return decision({ wake: true, reason: "PR_READY_FOR_REVIEW", priority: "high", ...base });
    }
    if (action === "closed" && pr?.merged === true) {
      return decision({ wake: true, reason: "PR_MERGED", priority: "high", ...base });
    }
    if (action === "closed") {
      return decision({ wake: true, reason: "PR_CLOSED_WITHOUT_MERGE", priority: "medium", ...base });
    }
    if (action === "opened") {
      return decision({ wake: true, reason: "PR_OPENED", priority: "medium", ...base });
    }
    if (action === "reopened") {
      return decision({ wake: true, reason: "PR_REOPENED", priority: "medium", ...base });
    }
    if (action === "converted_to_draft") {
      return decision({ wake: true, reason: "PR_CONVERTED_TO_DRAFT", priority: "medium", ...base });
    }
    return decision({ wake: false, reason: "PR_ACTION_NOT_RELEVANT", ...base });
  }

  if (event === "issue_comment") {
    const body = payload?.comment?.body || "";
    if (isLeaderControlBody(body) || isConfiguredLeaderActor(payload, leaderGithubLogin)) {
      return decision({ wake: false, reason: "LEADER_SELF_COMMENT_IGNORED", issueNumber, prNumber });
    }
    if (action === "deleted") {
      return decision({ wake: false, reason: "COMMENT_DELETED", issueNumber, prNumber });
    }
    if (action !== "created" && action !== "edited") {
      return decision({ wake: false, reason: "COMMENT_ACTION_NOT_RELEVANT", issueNumber, prNumber });
    }
    const signal = writerSignal(body);
    if (signal === "WRITER_FINAL") {
      return decision({ wake: true, reason: "WRITER_FINAL_SIGNAL", priority: "high", signal, issueNumber, prNumber });
    }
    if (signal === "WRITER_BLOCKED") {
      return decision({ wake: true, reason: "WRITER_BLOCKED_SIGNAL", priority: "high", signal, issueNumber, prNumber });
    }
    if (signal === "WRITER_PROGRESS") {
      return decision({ wake: false, reason: "WRITER_PROGRESS_COALESCE_CANDIDATE", priority: "low", signal, issueNumber, prNumber });
    }
    return decision({ wake: false, reason: "ORDINARY_COMMENT", issueNumber, prNumber });
  }

  if (event === "pull_request_review") {
    const body = payload?.review?.body || "";
    const headSha = payload?.pull_request?.head?.sha || "";
    if (isLeaderControlBody(body) || isConfiguredLeaderActor(payload, leaderGithubLogin)) {
      return decision({ wake: false, reason: "LEADER_SELF_REVIEW_IGNORED", headSha, prNumber });
    }
    if (action === "submitted" || action === "dismissed" || action === "edited") {
      return decision({ wake: true, reason: "PR_REVIEW_CHANGED", priority: "high", headSha, prNumber });
    }
    return decision({ wake: false, reason: "REVIEW_ACTION_NOT_RELEVANT", headSha, prNumber });
  }

  if (event === "pull_request_review_comment") {
    const body = payload?.comment?.body || "";
    const headSha = payload?.pull_request?.head?.sha || "";
    if (isLeaderControlBody(body) || isConfiguredLeaderActor(payload, leaderGithubLogin)) {
      return decision({ wake: false, reason: "LEADER_SELF_REVIEW_COMMENT_IGNORED", headSha, prNumber });
    }
    if (action !== "created" && action !== "edited") {
      return decision({ wake: false, reason: "REVIEW_COMMENT_ACTION_NOT_RELEVANT", headSha, prNumber });
    }
    const signal = writerSignal(body);
    if (signal === "WRITER_FINAL") {
      return decision({ wake: true, reason: "WRITER_FINAL_REVIEW_COMMENT", priority: "high", signal, headSha, prNumber });
    }
    if (signal === "WRITER_BLOCKED") {
      return decision({ wake: true, reason: "WRITER_BLOCKED_REVIEW_COMMENT", priority: "high", signal, headSha, prNumber });
    }
    if (signal === "WRITER_PROGRESS") {
      return decision({ wake: false, reason: "WRITER_PROGRESS_REVIEW_COMMENT", priority: "low", signal, headSha, prNumber });
    }
    return decision({ wake: true, reason: "PR_REVIEW_COMMENT_CHANGED", priority: "medium", headSha, prNumber });
  }

  if (event === "workflow_run") {
    const workflowName = payload?.workflow_run?.name || "";
    const headSha = payload?.workflow_run?.head_sha || "";
    if (action !== "completed") {
      return decision({ wake: false, reason: "WORKFLOW_NOT_COMPLETED", headSha, prNumber });
    }
    if (!IMPORTANT_WORKFLOWS.has(workflowName)) {
      return decision({ wake: false, reason: "WORKFLOW_NOT_ALLOWLISTED", headSha, prNumber });
    }
    return decision({ wake: true, reason: "IMPORTANT_WORKFLOW_COMPLETED", priority: "high", signal: workflowName, headSha, prNumber });
  }

  return decision({ wake: false, accepted: false, reason: "UNSUPPORTED_EVENT" });
}

export function getCoordinationLane(event, payload, result) {
  if (!result?.wake) {
    return "";
  }
  if (result.prNumber) {
    return `pr:${result.prNumber}`;
  }
  if (result.issueNumber) {
    return `issue:${result.issueNumber}`;
  }
  const defaultBranch = payload?.repository?.default_branch || DEFAULT_BRANCH;
  if (event === "push") {
    return `branch:${defaultBranch}`;
  }
  if (event === "workflow_run" && result.headSha) {
    return `workflow:${result.headSha}`;
  }
  return `repo:${defaultBranch}`;
}

export function leaseAllowsDelivery(lease, requestedOwner) {
  return lease?.acquired === true || lease?.owner === requestedOwner;
}

export function buildDispatchEnvelope({
  delivery,
  event,
  payload,
  result,
  coordinationLane,
  leaseOwner,
  observedAt,
}) {
  return {
    schema_version: DISPATCH_SCHEMA_VERSION,
    delivery,
    repository: EXPECTED_REPOSITORY,
    event,
    action: payload?.action || "",
    pr_number: result.prNumber,
    issue_number: result.issueNumber,
    head_sha: result.headSha || "",
    signal: result.signal || "",
    priority: result.priority || "none",
    reason: result.reason || "",
    coordination_lane: coordinationLane,
    lease_owner: leaseOwner || "",
    observed_at: observedAt,
    authority: "WAKE_SIGNAL_ONLY",
    mode: "OBSERVE_ONLY",
  };
}

export function validateDispatchEnvelope(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  if (value.schema_version !== DISPATCH_SCHEMA_VERSION) return false;
  if (value.repository !== EXPECTED_REPOSITORY) return false;
  if (typeof value.delivery !== "string" || !/^[A-Za-z0-9-]{16,128}$/.test(value.delivery)) return false;
  if (typeof value.event !== "string" || !SUPPORTED_EVENTS.has(value.event)) return false;
  if (typeof value.action !== "string" || value.action.length > 120) return false;
  if (value.pr_number !== null && (!Number.isInteger(value.pr_number) || value.pr_number < 1)) return false;
  if (value.issue_number !== null && (!Number.isInteger(value.issue_number) || value.issue_number < 1)) return false;
  if (typeof value.head_sha !== "string" || value.head_sha.length > 80) return false;
  if (typeof value.signal !== "string" || value.signal.length > 120) return false;
  if (!["none", "low", "medium", "high"].includes(value.priority)) return false;
  if (typeof value.reason !== "string" || value.reason.length > 120) return false;
  if (typeof value.coordination_lane !== "string" || !/^[A-Za-z0-9_.:/@+-]{1,180}$/.test(value.coordination_lane)) return false;
  if (typeof value.lease_owner !== "string" || !/^delivery:[A-Za-z0-9-]{16,128}$/.test(value.lease_owner)) return false;
  if (typeof value.observed_at !== "string" || Number.isNaN(Date.parse(value.observed_at))) return false;
  return value.authority === "WAKE_SIGNAL_ONLY" && value.mode === "OBSERVE_ONLY";
}

export function shouldSendToQueue({ dispatchState, lease, requestedOwner }) {
  if (!leaseAllowsDelivery(lease, requestedOwner)) {
    return false;
  }
  return dispatchState !== "queued";
}
