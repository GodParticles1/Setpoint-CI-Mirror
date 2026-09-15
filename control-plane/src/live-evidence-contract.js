import {
  GitHubReadError,
  createReadSession,
} from "./github-read-tools.js";

const COMPARE_COMMIT_LIMIT = 250;
const COMPARE_FILE_LIMIT = 300;

function pairKey(baseSha, headSha) {
  return `${baseSha}...${headSha}`;
}

function laneKind(envelope) {
  if (Number.isInteger(envelope?.pr_number)) return "pull_request";
  if (Number.isInteger(envelope?.issue_number)) return "issue";
  if (envelope?.event === "push") return "live_main_push";
  if (envelope?.event === "workflow_run") return "workflow";
  return "unknown";
}

function isWriterFinalIssue(envelope) {
  return laneKind(envelope) === "issue" && envelope?.signal === "WRITER_FINAL";
}

function prEvidenceMissing(state, contract, prNumber) {
  const missing = [];
  const head = state.prHeads.get(prNumber);
  if (!head) missing.push("PR_EXACT_HEAD");
  if (state.prBodiesComplete.get(prNumber) !== true) missing.push("PR_BODY_COMPLETE");
  if (!state.evidence.prFilesEnumerated.has(prNumber)) missing.push("PR_CHANGED_FILES_COMPLETE");
  const unresolved = state.prUnresolvedDiffPaths.get(prNumber);
  if (unresolved && unresolved.size > 0) missing.push("PR_DIFF_CONTENT_COMPLETE");
  if (head && !state.evidence.workflowRunsComplete.has(head)) missing.push("PR_EXACT_HEAD_WORKFLOW_RUNS");
  if (head && !state.evidence.commitStatusComplete.has(head)) missing.push("PR_EXACT_HEAD_STATUS_CHECKS");
  const runIds = head ? (state.evidence.workflowRunIdsBySha.get(head) || new Set()) : new Set();
  for (const runId of runIds) {
    if (!state.evidence.workflowJobsComplete.has(runId)) missing.push(`WORKFLOW_JOBS_${runId}`);
  }
  if (head && state.liveMain && !contract.safeComparePairs.has(pairKey(state.liveMain.main_sha, head))) {
    missing.push("LIVE_MAIN_PR_COMPARE");
  }
  return missing;
}

function shaExecutionEvidenceMissing(state, sha, prefix) {
  const missing = [];
  if (!sha) return [`${prefix}_SHA`];
  if (!state.evidence.workflowRunsComplete.has(sha)) missing.push(`${prefix}_WORKFLOW_RUNS`);
  if (!state.evidence.commitStatusComplete.has(sha)) missing.push(`${prefix}_STATUS_CHECKS`);
  const runIds = state.evidence.workflowRunIdsBySha.get(sha) || new Set();
  for (const runId of runIds) {
    if (!state.evidence.workflowJobsComplete.has(runId)) missing.push(`WORKFLOW_JOBS_${runId}`);
  }
  return missing;
}

function guardedClient(client) {
  return {
    async request(path) {
      const payload = await client.request(path);
      if (typeof path === "string" && path.includes("/compare/")) {
        const files = Array.isArray(payload?.files) ? payload.files : [];
        const commits = Number.isInteger(payload?.total_commits) ? payload.total_commits : null;
        if (commits === null || commits >= COMPARE_COMMIT_LIMIT || files.length >= COMPARE_FILE_LIMIT) {
          throw new GitHubReadError("COMPARE_EVIDENCE_INCOMPLETE", { fatal: true });
        }
      }
      return payload;
    },
  };
}

export function createEvidenceContractSession(options) {
  const base = createReadSession({ ...options, client: guardedClient(options.client) });
  const envelope = options?.envelope || {};
  const contract = {
    laneKind: laneKind(envelope),
    affectedIssueBodyComplete: null,
    linkedPrDiscoveryComplete: false,
    linkedPrNumbers: new Set(),
    linkedPrMetadata: new Map(),
    safeComparePairs: new Set(),
  };

  async function bootstrap() {
    const evidence = await base.bootstrap();
    if (contract.laneKind === "issue") {
      contract.affectedIssueBodyComplete = evidence?.affected?.value?.body_complete === true;
    }
    return {
      ...evidence,
      evidence_contract: {
        lane_kind: contract.laneKind,
        writer_final_issue: isWriterFinalIssue(envelope),
      },
    };
  }

  async function execute(name, args) {
    const result = await base.execute(name, args);

    if (name === "setpoint_get_issue_linked_prs" && args?.issue_number === base.state.affectedIssueNumber) {
      contract.linkedPrDiscoveryComplete = true;
      for (const item of result?.data?.items || []) {
        if (Number.isInteger(item?.pr_number)) contract.linkedPrNumbers.add(item.pr_number);
      }
    }

    if (name === "setpoint_get_pr" && Number.isInteger(args?.pr_number)) {
      contract.linkedPrMetadata.set(args.pr_number, result?.data || null);
    }

    if (name === "setpoint_compare") {
      const data = result?.data;
      const files = Array.isArray(data?.files) ? data.files : [];
      const commits = Number.isInteger(data?.total_commits) ? data.total_commits : null;
      const key = pairKey(args?.base_sha || "", args?.head_sha || "");
      const provablyComplete = data?.complete === true
        && commits !== null
        && commits < COMPARE_COMMIT_LIMIT
        && files.length < COMPARE_FILE_LIMIT;
      if (!provablyComplete) {
        base.state.evidence.comparePairsComplete.delete(key);
        throw new GitHubReadError("COMPARE_EVIDENCE_INCOMPLETE", { fatal: true });
      }
      contract.safeComparePairs.add(key);
    }

    return result;
  }

  function missingEvidence() {
    const state = base.state;
    const missing = [];
    if (!state.liveMain) missing.push("LIVE_MAIN");

    if (contract.laneKind === "pull_request") {
      missing.push(...prEvidenceMissing(state, contract, state.affectedPrNumber));
      return [...new Set(missing)];
    }

    if (contract.laneKind === "issue") {
      if (contract.affectedIssueBodyComplete !== true) missing.push("AFFECTED_ISSUE_BODY_COMPLETE");
      if (state.affectedIssueNumber && !state.evidence.issueCommentsComplete.has(state.affectedIssueNumber)) {
        missing.push("AFFECTED_ISSUE_COMMENTS");
      }
      if (isWriterFinalIssue(envelope)) {
        if (!contract.linkedPrDiscoveryComplete) missing.push("LINKED_PR_DISCOVERY");
        if (contract.linkedPrNumbers.size === 0) missing.push("REVIEWABLE_CANDIDATE_UNRESOLVED");
        if (contract.linkedPrNumbers.size > 0) {
          const missingMetadata = [...contract.linkedPrNumbers].filter((prNumber) => !contract.linkedPrMetadata.has(prNumber));
          if (missingMetadata.length > 0) missing.push("LINKED_PR_METADATA");
          const reviewable = [...contract.linkedPrNumbers].filter((prNumber) => contract.linkedPrMetadata.get(prNumber)?.state === "open");
          if (missingMetadata.length === 0 && reviewable.length !== 1) missing.push("REVIEWABLE_CANDIDATE_UNRESOLVED");
          if (reviewable.length === 1) missing.push(...prEvidenceMissing(state, contract, reviewable[0]));
        }
      }
      return [...new Set(missing)];
    }

    if (contract.laneKind === "live_main_push") {
      missing.push(...shaExecutionEvidenceMissing(state, state.liveMain?.main_sha, "LIVE_MAIN"));
      return [...new Set(missing)];
    }

    if (contract.laneKind === "workflow") {
      if (envelope?.head_sha !== state.liveMain?.main_sha) {
        missing.push("WORKFLOW_HINT_NOT_LIVE_RESOLVED");
      } else {
        missing.push(...shaExecutionEvidenceMissing(state, state.liveMain?.main_sha, "WORKFLOW"));
      }
      return [...new Set(missing)];
    }

    return [...new Set([...missing, "AFFECTED_LANE_UNRESOLVED"] )];
  }

  function verifyEvidenceComplete() {
    const missing = missingEvidence();
    if (missing.length > 0) throw new GitHubReadError("INCOMPLETE_LIVE_EVIDENCE", { fatal: true });
    return { complete: true, missing: [] };
  }

  return {
    state: base.state,
    contract,
    bootstrap,
    execute,
    verifyFresh: base.verifyFresh,
    verifyEvidenceComplete,
    missingEvidence,
  };
}
