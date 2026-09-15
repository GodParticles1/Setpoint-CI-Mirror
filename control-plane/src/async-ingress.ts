import { DurableObject } from "cloudflare:workers";
import {
  COORDINATOR_LEASE_TTL_SECONDS,
  EXPECTED_REPOSITORY,
  MAX_BODY_BYTES,
  SUPPORTED_EVENTS,
  WEBHOOK_PATH,
  buildDispatchEnvelope,
  evaluateEvent,
  getActorLogin,
  getCandidateHeadSha,
  getCoordinationLane,
  getIssueNumber,
  getPrNumber,
  leaseAllowsDelivery,
  shouldSendToQueue,
  validateDispatchEnvelope,
} from "./async-policy.js";

const OUTBOX_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
const DELIVERY_RE = /^[A-Za-z0-9-]{16,128}$/;

type DispatchEnvelope = ReturnType<typeof buildDispatchEnvelope>;
type DispatchState = "pending" | "queued";

interface AsyncIngressEnv {
  WEBHOOK_SECRET: string;
  LEADER_GITHUB_LOGIN?: string;
  CONTROL_PLANE_COORDINATOR: DurableObjectNamespace;
  ASYNC_DISPATCH_OUTBOX: DurableObjectNamespace;
  LEADER_QUEUE: Queue<DispatchEnvelope>;
}

interface DeliveryState {
  accepted: boolean;
  duplicate: boolean;
  firstSeenAt: string;
  expiresAt: string;
}

interface LeaseState {
  acquired: boolean;
  lane: string;
  owner: string;
  token?: string;
  expiresAt: string;
}

interface CoordinatorRpc {
  registerDelivery(input: {
    delivery: string;
    event: string;
    action: string;
    prNumber: number | null;
    issueNumber: number | null;
    headSha: string;
  }): Promise<DeliveryState>;
  acquireLease(input: {
    lane: string;
    owner: string;
    ttlSeconds: number;
  }): Promise<LeaseState>;
}

interface OutboxRecord {
  delivery: string;
  state: DispatchState;
  envelope: DispatchEnvelope;
  enqueueAttempts: number;
  lastErrorCode: string;
  firstSeenAt: string;
  updatedAt: string;
  queuedAt: string;
}

interface OutboxRpc {
  prepareDispatch(input: { envelope: DispatchEnvelope }): Promise<OutboxRecord>;
  markEnqueueAttempt(input: { delivery: string }): Promise<OutboxRecord>;
  markQueued(input: { delivery: string }): Promise<OutboxRecord>;
  recordEnqueueFailure(input: {
    delivery: string;
    errorCode: string;
  }): Promise<OutboxRecord>;
}

interface OutboxRow {
  delivery: string;
  envelope_json: string;
  state: DispatchState;
  enqueue_attempts: number;
  last_error_code: string;
  first_seen_ms: number;
  updated_at_ms: number;
  queued_at_ms: number | null;
  expires_at_ms: number;
}

function iso(ms: number): string {
  return new Date(ms).toISOString();
}

function jsonResponse(data: unknown, status = 202, extraHeaders?: HeadersInit): Response {
  return new Response(JSON.stringify(data, null, 2), {
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
      ...extraHeaders,
    },
  });
}

function hexToBytes(hex: string): Uint8Array | null {
  if (!/^[0-9a-fA-F]{64}$/.test(hex)) {
    return null;
  }
  const bytes = new Uint8Array(32);
  for (let i = 0; i < 32; i += 1) {
    bytes[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return bytes;
}

function constantTimeEqual(a: Uint8Array | null, b: Uint8Array): boolean {
  if (!a || a.length !== b.length) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < a.length; i += 1) {
    diff |= a[i] ^ b[i];
  }
  return diff === 0;
}

async function calculateSignature(secret: string, body: ArrayBuffer): Promise<Uint8Array> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return new Uint8Array(await crypto.subtle.sign("HMAC", key, body));
}

function safeLog(data: Record<string, unknown>): void {
  console.log(JSON.stringify(data));
}

function normalizeRepository(repository: string): string {
  return repository.trim().toLowerCase();
}

function rowToRecord(row: OutboxRow): OutboxRecord {
  const envelope = JSON.parse(row.envelope_json) as DispatchEnvelope;
  if (!validateDispatchEnvelope(envelope)) {
    throw new Error("stored dispatch envelope failed validation");
  }
  return {
    delivery: row.delivery,
    state: row.state,
    envelope,
    enqueueAttempts: Number(row.enqueue_attempts || 0),
    lastErrorCode: row.last_error_code || "",
    firstSeenAt: iso(row.first_seen_ms),
    updatedAt: iso(row.updated_at_ms),
    queuedAt: row.queued_at_ms ? iso(row.queued_at_ms) : "",
  };
}

export class AsyncDispatchOutbox extends DurableObject<AsyncIngressEnv> {
  constructor(ctx: DurableObjectState, env: AsyncIngressEnv) {
    super(ctx, env);
    this.ctx.storage.sql.exec(`
      CREATE TABLE IF NOT EXISTS async_dispatches (
        delivery TEXT PRIMARY KEY,
        envelope_json TEXT NOT NULL,
        state TEXT NOT NULL CHECK(state IN ('pending', 'queued')),
        enqueue_attempts INTEGER NOT NULL,
        last_error_code TEXT NOT NULL,
        first_seen_ms INTEGER NOT NULL,
        updated_at_ms INTEGER NOT NULL,
        queued_at_ms INTEGER,
        expires_at_ms INTEGER NOT NULL
      );

      CREATE INDEX IF NOT EXISTS idx_async_dispatches_expires
        ON async_dispatches(expires_at_ms);
    `);
  }

  private cleanup(now: number): void {
    this.ctx.storage.sql.exec(
      "DELETE FROM async_dispatches WHERE expires_at_ms <= ?",
      now,
    );
  }

  private getRow(delivery: string): OutboxRow | undefined {
    return this.ctx.storage.sql
      .exec(
        `SELECT delivery, envelope_json, state, enqueue_attempts,
                last_error_code, first_seen_ms, updated_at_ms,
                queued_at_ms, expires_at_ms
           FROM async_dispatches
          WHERE delivery = ?`,
        delivery,
      )
      .toArray()[0] as unknown as OutboxRow | undefined;
  }

  async prepareDispatch(input: { envelope: DispatchEnvelope }): Promise<OutboxRecord> {
    const { envelope } = input;
    if (!validateDispatchEnvelope(envelope)) {
      throw new Error("invalid dispatch envelope");
    }
    if (!DELIVERY_RE.test(envelope.delivery)) {
      throw new Error("invalid delivery");
    }

    const now = Date.now();
    this.cleanup(now);
    const serialized = JSON.stringify(envelope);
    const existing = this.getRow(envelope.delivery);

    if (existing) {
      if (existing.envelope_json !== serialized) {
        throw new Error("dispatch metadata conflict");
      }
      return rowToRecord(existing);
    }

    this.ctx.storage.sql.exec(
      `INSERT INTO async_dispatches (
         delivery, envelope_json, state, enqueue_attempts,
         last_error_code, first_seen_ms, updated_at_ms,
         queued_at_ms, expires_at_ms
       ) VALUES (?, ?, 'pending', 0, '', ?, ?, NULL, ?)`,
      envelope.delivery,
      serialized,
      now,
      now,
      now + OUTBOX_RETENTION_MS,
    );

    return rowToRecord(this.getRow(envelope.delivery)!);
  }

  async markEnqueueAttempt(input: { delivery: string }): Promise<OutboxRecord> {
    const now = Date.now();
    this.ctx.storage.sql.exec(
      `UPDATE async_dispatches
          SET enqueue_attempts = enqueue_attempts + 1,
              updated_at_ms = ?,
              last_error_code = ''
        WHERE delivery = ?`,
      now,
      input.delivery,
    );
    const row = this.getRow(input.delivery);
    if (!row) throw new Error("dispatch not found");
    return rowToRecord(row);
  }

  async markQueued(input: { delivery: string }): Promise<OutboxRecord> {
    const now = Date.now();
    this.ctx.storage.sql.exec(
      `UPDATE async_dispatches
          SET state = 'queued',
              queued_at_ms = COALESCE(queued_at_ms, ?),
              updated_at_ms = ?,
              last_error_code = ''
        WHERE delivery = ?`,
      now,
      now,
      input.delivery,
    );
    const row = this.getRow(input.delivery);
    if (!row) throw new Error("dispatch not found");
    return rowToRecord(row);
  }

  async recordEnqueueFailure(input: {
    delivery: string;
    errorCode: string;
  }): Promise<OutboxRecord> {
    const now = Date.now();
    const errorCode = /^[A-Z0-9_]{1,64}$/.test(input.errorCode)
      ? input.errorCode
      : "ENQUEUE_ERROR";
    this.ctx.storage.sql.exec(
      `UPDATE async_dispatches
          SET state = 'pending',
              updated_at_ms = ?,
              last_error_code = ?
        WHERE delivery = ?`,
      now,
      errorCode,
      input.delivery,
    );
    const row = this.getRow(input.delivery);
    if (!row) throw new Error("dispatch not found");
    return rowToRecord(row);
  }
}

export default {
  async fetch(request, env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname !== WEBHOOK_PATH) {
      return new Response("Not Found", { status: 404 });
    }
    if (request.method !== "POST") {
      return new Response("Method Not Allowed", {
        status: 405,
        headers: { Allow: "POST" },
      });
    }
    if (!env.WEBHOOK_SECRET) {
      return new Response("Server configuration error", { status: 500 });
    }

    const delivery = request.headers.get("X-GitHub-Delivery") || "";
    const event = request.headers.get("X-GitHub-Event") || "";
    if (!DELIVERY_RE.test(delivery) || !event) {
      return new Response("Missing or invalid GitHub headers", { status: 400 });
    }

    const contentLength = Number(request.headers.get("content-length") || "0");
    if (Number.isFinite(contentLength) && contentLength > MAX_BODY_BYTES) {
      return new Response("Payload Too Large", { status: 413 });
    }

    const body = await request.arrayBuffer();
    if (body.byteLength > MAX_BODY_BYTES) {
      return new Response("Payload Too Large", { status: 413 });
    }

    const signatureHeader = request.headers.get("X-Hub-Signature-256");
    if (!signatureHeader?.startsWith("sha256=")) {
      return new Response("Forbidden", { status: 403 });
    }
    const suppliedSignature = hexToBytes(signatureHeader.slice("sha256=".length));
    const expectedSignature = await calculateSignature(env.WEBHOOK_SECRET, body);
    if (!constantTimeEqual(suppliedSignature, expectedSignature)) {
      return new Response("Forbidden", { status: 403 });
    }

    let payload: Record<string, any>;
    try {
      payload = JSON.parse(new TextDecoder().decode(body)) as Record<string, any>;
    } catch {
      return new Response("Invalid JSON", { status: 400 });
    }

    const repository = payload?.repository?.full_name;
    if (repository !== EXPECTED_REPOSITORY) {
      return new Response("Forbidden", { status: 403 });
    }
    if (!env.CONTROL_PLANE_COORDINATOR || !env.ASYNC_DISPATCH_OUTBOX || !env.LEADER_QUEUE) {
      return new Response("Async boundary binding is not configured", { status: 500 });
    }

    const normalizedRepository = normalizeRepository(repository);
    const coordinator = env.CONTROL_PLANE_COORDINATOR.getByName(
      normalizedRepository,
    ) as unknown as CoordinatorRpc;
    const outbox = env.ASYNC_DISPATCH_OUTBOX.getByName(
      normalizedRepository,
    ) as unknown as OutboxRpc;

    const supported = SUPPORTED_EVENTS.has(event);
    const result = supported
      ? evaluateEvent(event, payload, env.LEADER_GITHUB_LOGIN || "")
      : {
          wake: false,
          accepted: false,
          reason: "UNSUPPORTED_EVENT",
          priority: "none",
          signal: "",
          ref: "",
          headSha: "",
          prNumber: null,
          issueNumber: null,
        };
    const coordinationLane = getCoordinationLane(event, payload, result);

    let deliveryState: DeliveryState;
    try {
      deliveryState = await coordinator.registerDelivery({
        delivery,
        event,
        action: payload?.action || "",
        prNumber: getPrNumber(event, payload),
        issueNumber: getIssueNumber(event, payload),
        headSha: getCandidateHeadSha(event, payload),
      });
    } catch {
      return jsonResponse(
        {
          ok: false,
          delivery,
          event,
          repository,
          wake: false,
          reason: "COORDINATOR_DELIVERY_ERROR",
          mode: "OBSERVE_ONLY",
        },
        503,
      );
    }

    if (event === "ping") {
      return new Response(null, { status: 204 });
    }

    if (!supported || !result.wake || !coordinationLane) {
      const reason = deliveryState.duplicate
        ? "DUPLICATE_DELIVERY"
        : result.reason;
      safeLog({
        type: "github_webhook",
        delivery,
        event,
        repository,
        duplicate: deliveryState.duplicate,
        wake: false,
        reason,
        signature: "valid",
      });
      return jsonResponse({
        ok: true,
        delivery,
        event,
        action: payload?.action || "",
        repository,
        duplicate: deliveryState.duplicate,
        wake: false,
        reason,
        mode: "OBSERVE_ONLY",
        async_enqueued: false,
      });
    }

    const requestedOwner = `delivery:${delivery}`;
    let lease: LeaseState;
    try {
      lease = await coordinator.acquireLease({
        lane: coordinationLane,
        owner: requestedOwner,
        ttlSeconds: COORDINATOR_LEASE_TTL_SECONDS,
      });
    } catch {
      return jsonResponse(
        {
          ok: false,
          delivery,
          event,
          repository,
          duplicate: deliveryState.duplicate,
          wake: result.wake,
          reason: "COORDINATOR_LEASE_ERROR",
          coordination_lane: coordinationLane,
          mode: "OBSERVE_ONLY",
        },
        503,
      );
    }

    const leaseEligible = leaseAllowsDelivery(lease, requestedOwner);
    if (!leaseEligible) {
      safeLog({
        type: "github_webhook",
        delivery,
        event,
        repository,
        duplicate: deliveryState.duplicate,
        wake: result.wake,
        reason: result.reason,
        coordination_lane: coordinationLane,
        lease_acquired: false,
        lease_owner: lease.owner,
        async_enqueued: false,
      });
      return jsonResponse({
        ok: true,
        delivery,
        event,
        action: payload?.action || "",
        repository,
        pr_number: result.prNumber,
        issue_number: result.issueNumber,
        head_sha: result.headSha,
        signal: result.signal,
        priority: result.priority,
        wake: result.wake,
        reason: result.reason,
        duplicate: deliveryState.duplicate,
        coordination_lane: coordinationLane,
        lease_acquired: false,
        lease_owner: lease.owner,
        lease_expires_at: lease.expiresAt,
        mode: "OBSERVE_ONLY",
        async_enqueued: false,
      });
    }

    const envelope = buildDispatchEnvelope({
      delivery,
      event,
      payload,
      result,
      coordinationLane,
      leaseOwner: requestedOwner,
      observedAt: deliveryState.firstSeenAt,
    });

    let dispatch: OutboxRecord;
    try {
      dispatch = await outbox.prepareDispatch({ envelope });
    } catch {
      return jsonResponse(
        {
          ok: false,
          delivery,
          event,
          repository,
          duplicate: deliveryState.duplicate,
          wake: result.wake,
          reason: "OUTBOX_PREPARE_ERROR",
          coordination_lane: coordinationLane,
          mode: "OBSERVE_ONLY",
        },
        503,
      );
    }

    if (!shouldSendToQueue({
      dispatchState: dispatch.state,
      lease,
      requestedOwner,
    })) {
      return jsonResponse({
        ok: true,
        delivery,
        event,
        action: payload?.action || "",
        repository,
        duplicate: true,
        wake: false,
        reason: "DUPLICATE_DELIVERY",
        coordination_lane: coordinationLane,
        lease_acquired: true,
        lease_owner: requestedOwner,
        mode: "OBSERVE_ONLY",
        async_enqueued: false,
        async_state: dispatch.state,
      });
    }

    try {
      await outbox.markEnqueueAttempt({ delivery });
    } catch {
      safeLog({
        type: "async_outbox_warning",
        delivery,
        repository,
        code: "OUTBOX_ATTEMPT_MARK_FAILED",
      });
    }

    try {
      await env.LEADER_QUEUE.send(dispatch.envelope);
    } catch {
      try {
        await outbox.recordEnqueueFailure({
          delivery,
          errorCode: "QUEUE_SEND_FAILED",
        });
      } catch {
        safeLog({
          type: "async_outbox_warning",
          delivery,
          repository,
          code: "OUTBOX_FAILURE_MARK_FAILED",
        });
      }
      safeLog({
        type: "github_webhook",
        delivery,
        event,
        repository,
        duplicate: deliveryState.duplicate,
        wake: result.wake,
        reason: "QUEUE_ENQUEUE_FAILED",
        coordination_lane: coordinationLane,
        async_enqueued: false,
      });
      return jsonResponse(
        {
          ok: false,
          delivery,
          event,
          repository,
          duplicate: deliveryState.duplicate,
          wake: result.wake,
          reason: "QUEUE_ENQUEUE_FAILED",
          coordination_lane: coordinationLane,
          mode: "OBSERVE_ONLY",
          async_enqueued: false,
          recovery: "REDELIVER_SAME_GITHUB_DELIVERY",
        },
        503,
        { "retry-after": "5" },
      );
    }

    let outboxMarkedQueued = true;
    try {
      await outbox.markQueued({ delivery });
    } catch {
      outboxMarkedQueued = false;
      safeLog({
        type: "async_outbox_warning",
        delivery,
        repository,
        code: "OUTBOX_QUEUE_MARK_FAILED",
      });
    }

    safeLog({
      type: "github_webhook",
      delivery,
      event,
      action: payload?.action || "",
      repository,
      actor: getActorLogin(payload),
      pr_number: result.prNumber,
      issue_number: result.issueNumber,
      head_sha: result.headSha,
      signal: result.signal,
      priority: result.priority,
      wake: result.wake,
      reason: result.reason,
      signature: "valid",
      duplicate: deliveryState.duplicate,
      coordination_lane: coordinationLane,
      lease_acquired: true,
      lease_owner: requestedOwner,
      lease_expires_at: lease.expiresAt,
      async_enqueued: true,
      outbox_marked_queued: outboxMarkedQueued,
    });

    return jsonResponse({
      ok: true,
      delivery,
      event,
      action: payload?.action || "",
      repository,
      pr_number: result.prNumber,
      issue_number: result.issueNumber,
      head_sha: result.headSha,
      signal: result.signal,
      priority: result.priority,
      wake: result.wake,
      reason: result.reason,
      duplicate: deliveryState.duplicate,
      coordination_lane: coordinationLane,
      lease_acquired: true,
      lease_owner: requestedOwner,
      lease_expires_at: lease.expiresAt,
      mode: "OBSERVE_ONLY",
      async_enqueued: true,
      async_boundary: "CLOUDFLARE_QUEUE",
      ack_boundary: "QUEUE_SEND_CONFIRMED",
      accepted_state_stored: false,
      outbox_marked_queued: outboxMarkedQueued,
    });
  },
} satisfies ExportedHandler<AsyncIngressEnv>;
