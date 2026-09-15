import { DurableObject } from "cloudflare:workers";
import {
  ALLOWED_OPENAI_MODELS,
  INVOCATION_STATES,
  createOpenAITransport,
  executeObserveOnly,
  selectOpenAIModel,
  validateWakeEnvelope,
} from "./openai-core.js";

const INVOCATION_RETENTION_MS = 14 * 24 * 60 * 60 * 1000;
const RESERVATION_STALE_MS = 60 * 1000;
const DISPATCH_STALE_MS = 15 * 60 * 1000;
const DELIVERY_RE = /^[A-Za-z0-9-]{16,128}$/;
const ERROR_CLASS_RE = /^[A-Z0-9_]{0,64}$/;

interface OpenAIConsumerEnv {
  OPENAI_API_KEY: string;
  OPENAI_MODEL?: string;
  MODEL_INVOCATION_REGISTRY: DurableObjectNamespace;
}

interface UsageMetadata {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
}

interface WakeEnvelope {
  schema_version: number;
  delivery: string;
  repository: string;
  event: string;
  action: string;
  pr_number: number | null;
  issue_number: number | null;
  head_sha: string;
  signal: string;
  priority: string;
  reason: string;
  coordination_lane: string;
  lease_owner: string;
  observed_at: string;
  authority: string;
  mode: string;
}

interface ReservationResult {
  action: "DISPATCH" | "REPLAY_COMPLETED" | "IN_FLIGHT" | "TERMINAL";
  state: string;
  responseId: string;
  errorClass: string;
}

interface RegistryRpc {
  reserve(input: { delivery: string; model: string }): Promise<ReservationResult>;
  markDispatching(input: { delivery: string }): Promise<void>;
  markPreDispatchFailure(input: { delivery: string; errorClass: string }): Promise<void>;
  markCompleted(input: { delivery: string; responseId: string; usage: UsageMetadata }): Promise<void>;
  markTerminal(input: { delivery: string; state: string; responseId: string; errorClass: string; usage: UsageMetadata }): Promise<void>;
  markUncertain(input: { delivery: string; responseId: string; errorClass: string; usage: UsageMetadata }): Promise<void>;
}

interface InvocationRow {
  delivery: string;
  model: string;
  status: string;
  attempts: number;
  response_id: string;
  error_class: string;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  first_seen_ms: number;
  updated_at_ms: number;
  dispatch_started_ms: number | null;
  completed_ms: number | null;
  expires_at_ms: number;
}

function safeErrorClass(value: string): string {
  const normalized = String(value || "").toUpperCase().replace(/[^A-Z0-9_]/g, "_").slice(0, 64);
  return ERROR_CLASS_RE.test(normalized) ? normalized : "UNKNOWN_ERROR";
}
function safeTokenCount(value: number): number { return Number.isInteger(value) && value >= 0 ? value : 0; }
function validateDelivery(delivery: string): void { if (!DELIVERY_RE.test(delivery)) throw new Error("invalid delivery"); }
function validateModel(model: string): void { if (!ALLOWED_OPENAI_MODELS.has(model)) throw new Error("model not allowlisted"); }
function safeLog(data: Record<string, unknown>): void { console.log(JSON.stringify(data)); }

export class ModelInvocationRegistry extends DurableObject<OpenAIConsumerEnv> {
  constructor(ctx: DurableObjectState, env: OpenAIConsumerEnv) {
    super(ctx, env);
    this.ctx.storage.sql.exec(`
      CREATE TABLE IF NOT EXISTS model_invocations (
        delivery TEXT PRIMARY KEY,
        model TEXT NOT NULL,
        status TEXT NOT NULL,
        attempts INTEGER NOT NULL,
        response_id TEXT NOT NULL,
        error_class TEXT NOT NULL,
        input_tokens INTEGER NOT NULL,
        output_tokens INTEGER NOT NULL,
        total_tokens INTEGER NOT NULL,
        first_seen_ms INTEGER NOT NULL,
        updated_at_ms INTEGER NOT NULL,
        dispatch_started_ms INTEGER,
        completed_ms INTEGER,
        expires_at_ms INTEGER NOT NULL
      );
      CREATE INDEX IF NOT EXISTS idx_model_invocations_expires ON model_invocations(expires_at_ms);
    `);
  }

  private cleanup(now: number): void { this.ctx.storage.sql.exec("DELETE FROM model_invocations WHERE expires_at_ms <= ?", now); }
  private getRow(delivery: string): InvocationRow | undefined {
    return this.ctx.storage.sql.exec(`SELECT delivery, model, status, attempts, response_id, error_class, input_tokens, output_tokens, total_tokens, first_seen_ms, updated_at_ms, dispatch_started_ms, completed_ms, expires_at_ms FROM model_invocations WHERE delivery = ?`, delivery).toArray()[0] as unknown as InvocationRow | undefined;
  }

  async reserve(input: { delivery: string; model: string }): Promise<ReservationResult> {
    validateDelivery(input.delivery); validateModel(input.model);
    const now = Date.now(); this.cleanup(now); const current = this.getRow(input.delivery);
    if (!current) {
      this.ctx.storage.sql.exec(`INSERT INTO model_invocations (delivery, model, status, attempts, response_id, error_class, input_tokens, output_tokens, total_tokens, first_seen_ms, updated_at_ms, dispatch_started_ms, completed_ms, expires_at_ms) VALUES (?, ?, ?, 0, '', '', 0, 0, 0, ?, ?, NULL, NULL, ?)`, input.delivery, input.model, INVOCATION_STATES.RESERVED, now, now, now + INVOCATION_RETENTION_MS);
      return { action: "DISPATCH", state: INVOCATION_STATES.RESERVED, responseId: "", errorClass: "" };
    }
    if (current.model !== input.model) return { action: "TERMINAL", state: INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH, responseId: current.response_id, errorClass: "MODEL_CONFLICT" };
    if (current.status === INVOCATION_STATES.COMPLETED) return { action: "REPLAY_COMPLETED", state: current.status, responseId: current.response_id, errorClass: current.error_class };
    if (current.status === INVOCATION_STATES.FAILED_PRE_DISPATCH) {
      this.ctx.storage.sql.exec(`UPDATE model_invocations SET status = ?, error_class = '', updated_at_ms = ? WHERE delivery = ?`, INVOCATION_STATES.RESERVED, now, input.delivery);
      return { action: "DISPATCH", state: INVOCATION_STATES.RESERVED, responseId: "", errorClass: "" };
    }
    if (current.status === INVOCATION_STATES.RESERVED) {
      if (now - current.updated_at_ms > RESERVATION_STALE_MS) {
        this.ctx.storage.sql.exec("UPDATE model_invocations SET updated_at_ms = ? WHERE delivery = ?", now, input.delivery);
        return { action: "DISPATCH", state: INVOCATION_STATES.RESERVED, responseId: "", errorClass: "" };
      }
      return { action: "IN_FLIGHT", state: current.status, responseId: current.response_id, errorClass: current.error_class };
    }
    if (current.status === INVOCATION_STATES.DISPATCHING) {
      const dispatchStarted = current.dispatch_started_ms || current.updated_at_ms;
      if (now - dispatchStarted > DISPATCH_STALE_MS) {
        const errorClass = "STALE_DISPATCH_UNCERTAIN";
        this.ctx.storage.sql.exec(`UPDATE model_invocations SET status = ?, error_class = ?, updated_at_ms = ? WHERE delivery = ?`, INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH, errorClass, now, input.delivery);
        return { action: "TERMINAL", state: INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH, responseId: current.response_id, errorClass };
      }
      return { action: "IN_FLIGHT", state: current.status, responseId: current.response_id, errorClass: current.error_class };
    }
    return { action: "TERMINAL", state: current.status, responseId: current.response_id, errorClass: current.error_class };
  }

  async markDispatching(input: { delivery: string }): Promise<void> {
    validateDelivery(input.delivery); const now = Date.now(); const current = this.getRow(input.delivery);
    if (!current || current.status !== INVOCATION_STATES.RESERVED) throw new Error("invocation is not reserved");
    this.ctx.storage.sql.exec(`UPDATE model_invocations SET status = ?, attempts = attempts + 1, dispatch_started_ms = ?, updated_at_ms = ?, error_class = '' WHERE delivery = ?`, INVOCATION_STATES.DISPATCHING, now, now, input.delivery);
  }

  async markPreDispatchFailure(input: { delivery: string; errorClass: string }): Promise<void> {
    validateDelivery(input.delivery); const now = Date.now(); const current = this.getRow(input.delivery);
    if (!current || current.status !== INVOCATION_STATES.RESERVED) throw new Error("invocation is not reserved");
    this.ctx.storage.sql.exec(`UPDATE model_invocations SET status = ?, error_class = ?, updated_at_ms = ? WHERE delivery = ?`, INVOCATION_STATES.FAILED_PRE_DISPATCH, safeErrorClass(input.errorClass), now, input.delivery);
  }

  async markCompleted(input: { delivery: string; responseId: string; usage: UsageMetadata }): Promise<void> { await this.finishInvocation({ ...input, state: INVOCATION_STATES.COMPLETED, errorClass: "" }); }
  async markTerminal(input: { delivery: string; state: string; responseId: string; errorClass: string; usage: UsageMetadata }): Promise<void> {
    if (![INVOCATION_STATES.REFUSED, INVOCATION_STATES.INCOMPLETE, INVOCATION_STATES.INVALID_OUTPUT].some((state) => state === input.state)) throw new Error("invalid terminal state");
    await this.finishInvocation(input);
  }
  async markUncertain(input: { delivery: string; responseId: string; errorClass: string; usage: UsageMetadata }): Promise<void> { await this.finishInvocation({ ...input, state: INVOCATION_STATES.UNCERTAIN_AFTER_DISPATCH }); }

  private async finishInvocation(input: { delivery: string; state: string; responseId: string; errorClass: string; usage: UsageMetadata }): Promise<void> {
    validateDelivery(input.delivery); const current = this.getRow(input.delivery);
    if (!current || current.status !== INVOCATION_STATES.DISPATCHING) throw new Error("invocation is not dispatching");
    const now = Date.now();
    this.ctx.storage.sql.exec(`UPDATE model_invocations SET status = ?, response_id = ?, error_class = ?, input_tokens = ?, output_tokens = ?, total_tokens = ?, updated_at_ms = ?, completed_ms = ? WHERE delivery = ?`, input.state, String(input.responseId || "").slice(0, 192), input.errorClass ? safeErrorClass(input.errorClass) : "", safeTokenCount(input.usage.inputTokens), safeTokenCount(input.usage.outputTokens), safeTokenCount(input.usage.totalTokens), now, now, input.delivery);
  }
}

export default {
  async queue(batch, env): Promise<void> {
    for (const message of batch.messages) {
      const rawEnvelope = message.body;
      if (!validateWakeEnvelope(rawEnvelope)) { safeLog({ type: "openai_observe_only", result: "INVALID_QUEUE_ENVELOPE" }); message.ack(); continue; }
      const envelope = rawEnvelope as WakeEnvelope;
      let model: string;
      try { model = selectOpenAIModel(env.OPENAI_MODEL); }
      catch { safeLog({ type: "openai_observe_only", delivery: envelope.delivery, result: "OPENAI_MODEL_NOT_ALLOWLISTED" }); message.retry({ delaySeconds: 30 }); continue; }
      const registry = env.MODEL_INVOCATION_REGISTRY.getByName(envelope.delivery) as unknown as RegistryRpc;
      const transport = createOpenAITransport({ apiKey: env.OPENAI_API_KEY, model });
      try {
        const outcome = await executeObserveOnly({ envelope, registry, transport });
        safeLog({ type: "openai_observe_only", delivery: envelope.delivery, state: outcome.state, replay: outcome.replay === true, response_id: outcome.responseId || "", error_class: outcome.errorClass || "", leader_decision: outcome.decision?.LEADER_DECISION || "", lane_state: outcome.decision?.LANE_STATE || "", candidate_state: outcome.decision?.CANDIDATE_STATE || "", blocker_class: outcome.decision?.BLOCKER_CLASS || "", github_mutation_count: 0 });
        if (outcome.queueAction === "retry") message.retry({ delaySeconds: 30 }); else message.ack();
      } catch {
        safeLog({ type: "openai_observe_only", delivery: envelope.delivery, result: "CONSUMER_INTERNAL_ERROR" });
        message.retry({ delaySeconds: 30 });
      }
    }
  },
} satisfies ExportedHandler<OpenAIConsumerEnv>;
