import { DurableObject } from "cloudflare:workers";

const SERVICE_NAME = "github-control-plane-coordinator";
const DELIVERY_RETENTION_SECONDS = 7 * 24 * 60 * 60;
const MIN_DELIVERY_RETENTION_SECONDS = 24 * 60 * 60;
const MAX_DELIVERY_RETENTION_SECONDS = 14 * 24 * 60 * 60;

const DEFAULT_LEASE_TTL_SECONDS = 5 * 60;
const MIN_LEASE_TTL_SECONDS = 30;
const MAX_LEASE_TTL_SECONDS = 30 * 60;

const REPOSITORY_RE = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
const DELIVERY_RE = /^[A-Za-z0-9-]{16,128}$/;
const LANE_RE = /^[A-Za-z0-9_.:/@+-]{1,180}$/;
const OWNER_RE = /^[A-Za-z0-9_.:/@+-]{1,120}$/;

type JsonObject = Record<string, unknown>;

interface DeliveryRegistrationInput {
  delivery: string;
  event: string;
  action?: string;
  prNumber?: number | null;
  issueNumber?: number | null;
  headSha?: string;
  retentionSeconds?: number;
}

interface DeliveryRegistrationResult {
  accepted: boolean;
  duplicate: boolean;
  firstSeenAt: string;
  expiresAt: string;
}

interface AcquireLeaseInput {
  lane: string;
  owner: string;
  ttlSeconds?: number;
}

interface AcquireLeaseResult {
  acquired: boolean;
  lane: string;
  owner: string;
  token?: string;
  expiresAt: string;
}

interface RenewLeaseInput {
  lane: string;
  token: string;
  ttlSeconds?: number;
}

interface RenewLeaseResult {
  renewed: boolean;
  lane: string;
  expiresAt?: string;
}

interface ReleaseLeaseInput {
  lane: string;
  token: string;
}

interface CoordinatorStatus {
  deliveryCount: number;
  activeLeaseCount: number;
  now: string;
}

interface DeliveryRow {
  first_seen_ms: number;
  expires_at_ms: number;
}

interface LeaseRow {
  owner: string;
  token: string;
  expires_at_ms: number;
}

function clampInteger(
  value: number | undefined,
  defaultValue: number,
  min: number,
  max: number,
): number {
  if (value === undefined || !Number.isFinite(value)) {
    return defaultValue;
  }

  return Math.max(min, Math.min(max, Math.trunc(value)));
}

function iso(ms: number): string {
  return new Date(ms).toISOString();
}

function requireString(
  body: JsonObject,
  key: string,
  pattern?: RegExp,
): string {
  const value = body[key];

  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`invalid ${key}`);
  }

  if (pattern && !pattern.test(value)) {
    throw new Error(`invalid ${key}`);
  }

  return value;
}

function optionalString(
  body: JsonObject,
  key: string,
  maxLength: number,
): string {
  const value = body[key];

  if (value === undefined || value === null) {
    return "";
  }

  if (typeof value !== "string" || value.length > maxLength) {
    throw new Error(`invalid ${key}`);
  }

  return value;
}

function optionalNullableInteger(
  body: JsonObject,
  key: string,
): number | null {
  const value = body[key];

  if (value === undefined || value === null) {
    return null;
  }

  if (
    typeof value !== "number" ||
    !Number.isInteger(value) ||
    value < 1
  ) {
    throw new Error(`invalid ${key}`);
  }

  return value;
}

function optionalInteger(
  body: JsonObject,
  key: string,
): number | undefined {
  const value = body[key];

  if (value === undefined || value === null) {
    return undefined;
  }

  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`invalid ${key}`);
  }

  return Math.trunc(value);
}

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data, null, 2), {
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}

async function readJsonObject(request: Request): Promise<JsonObject> {
  const contentLength = Number(
    request.headers.get("content-length") || "0",
  );

  if (Number.isFinite(contentLength) && contentLength > 64 * 1024) {
    throw new Error("request body too large");
  }

  const text = await request.text();

  if (text.length > 64 * 1024) {
    throw new Error("request body too large");
  }

  const parsed: unknown = JSON.parse(text);

  if (
    parsed === null ||
    typeof parsed !== "object" ||
    Array.isArray(parsed)
  ) {
    throw new Error("request body must be an object");
  }

  return parsed as JsonObject;
}

function normalizeRepository(repository: string): string {
  return repository.trim().toLowerCase();
}

export class ControlPlaneCoordinator extends DurableObject<Env> {
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);

    this.ctx.storage.sql.exec(`
      CREATE TABLE IF NOT EXISTS deliveries (
        delivery TEXT PRIMARY KEY,
        event TEXT NOT NULL,
        action TEXT NOT NULL,
        pr_number INTEGER,
        issue_number INTEGER,
        head_sha TEXT NOT NULL,
        first_seen_ms INTEGER NOT NULL,
        expires_at_ms INTEGER NOT NULL
      );

      CREATE INDEX IF NOT EXISTS idx_deliveries_expires
        ON deliveries(expires_at_ms);

      CREATE TABLE IF NOT EXISTS leases (
        lane TEXT PRIMARY KEY,
        owner TEXT NOT NULL,
        token TEXT NOT NULL,
        acquired_at_ms INTEGER NOT NULL,
        expires_at_ms INTEGER NOT NULL,
        updated_at_ms INTEGER NOT NULL
      );

      CREATE INDEX IF NOT EXISTS idx_leases_expires
        ON leases(expires_at_ms);
    `);
  }

  async registerDelivery(
    input: DeliveryRegistrationInput,
  ): Promise<DeliveryRegistrationResult> {
    const now = Date.now();

    const retentionSeconds = clampInteger(
      input.retentionSeconds,
      DELIVERY_RETENTION_SECONDS,
      MIN_DELIVERY_RETENTION_SECONDS,
      MAX_DELIVERY_RETENTION_SECONDS,
    );

    const expiresAt = now + retentionSeconds * 1000;

    this.ctx.storage.sql.exec(
      "DELETE FROM deliveries WHERE expires_at_ms <= ?",
      now,
    );

    const existing = this.ctx.storage.sql
      .exec(
        `
          SELECT first_seen_ms, expires_at_ms
          FROM deliveries
          WHERE delivery = ?
        `,
        input.delivery,
      )
      .toArray()[0] as unknown as DeliveryRow | undefined;

    if (existing) {
      return {
        accepted: false,
        duplicate: true,
        firstSeenAt: iso(existing.first_seen_ms),
        expiresAt: iso(existing.expires_at_ms),
      };
    }

    this.ctx.storage.sql.exec(
      `
        INSERT INTO deliveries (
          delivery,
          event,
          action,
          pr_number,
          issue_number,
          head_sha,
          first_seen_ms,
          expires_at_ms
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
      `,
      input.delivery,
      input.event,
      input.action || "",
      input.prNumber ?? null,
      input.issueNumber ?? null,
      input.headSha || "",
      now,
      expiresAt,
    );

    return {
      accepted: true,
      duplicate: false,
      firstSeenAt: iso(now),
      expiresAt: iso(expiresAt),
    };
  }

  async acquireLease(
    input: AcquireLeaseInput,
  ): Promise<AcquireLeaseResult> {
    const now = Date.now();

    const ttlSeconds = clampInteger(
      input.ttlSeconds,
      DEFAULT_LEASE_TTL_SECONDS,
      MIN_LEASE_TTL_SECONDS,
      MAX_LEASE_TTL_SECONDS,
    );

    this.ctx.storage.sql.exec(
      "DELETE FROM leases WHERE expires_at_ms <= ?",
      now,
    );

    const current = this.ctx.storage.sql
      .exec(
        `
          SELECT owner, token, expires_at_ms
          FROM leases
          WHERE lane = ?
        `,
        input.lane,
      )
      .toArray()[0] as unknown as LeaseRow | undefined;

    if (current && current.expires_at_ms > now) {
      return {
        acquired: false,
        lane: input.lane,
        owner: current.owner,
        expiresAt: iso(current.expires_at_ms),
      };
    }

    const token = crypto.randomUUID();
    const expiresAt = now + ttlSeconds * 1000;

    this.ctx.storage.sql.exec(
      `
        INSERT INTO leases (
          lane,
          owner,
          token,
          acquired_at_ms,
          expires_at_ms,
          updated_at_ms
        ) VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(lane) DO UPDATE SET
          owner = excluded.owner,
          token = excluded.token,
          acquired_at_ms = excluded.acquired_at_ms,
          expires_at_ms = excluded.expires_at_ms,
          updated_at_ms = excluded.updated_at_ms
      `,
      input.lane,
      input.owner,
      token,
      now,
      expiresAt,
      now,
    );

    return {
      acquired: true,
      lane: input.lane,
      owner: input.owner,
      token,
      expiresAt: iso(expiresAt),
    };
  }

  async renewLease(
    input: RenewLeaseInput,
  ): Promise<RenewLeaseResult> {
    const now = Date.now();

    const ttlSeconds = clampInteger(
      input.ttlSeconds,
      DEFAULT_LEASE_TTL_SECONDS,
      MIN_LEASE_TTL_SECONDS,
      MAX_LEASE_TTL_SECONDS,
    );

    const expiresAt = now + ttlSeconds * 1000;

    const current = this.ctx.storage.sql
      .exec(
        `
          SELECT owner, token, expires_at_ms
          FROM leases
          WHERE lane = ?
        `,
        input.lane,
      )
      .toArray()[0] as unknown as LeaseRow | undefined;

    if (
      !current ||
      current.token !== input.token ||
      current.expires_at_ms <= now
    ) {
      return {
        renewed: false,
        lane: input.lane,
      };
    }

    this.ctx.storage.sql.exec(
      `
        UPDATE leases
        SET expires_at_ms = ?, updated_at_ms = ?
        WHERE lane = ? AND token = ?
      `,
      expiresAt,
      now,
      input.lane,
      input.token,
    );

    return {
      renewed: true,
      lane: input.lane,
      expiresAt: iso(expiresAt),
    };
  }

  async releaseLease(
    input: ReleaseLeaseInput,
  ): Promise<{ released: boolean; lane: string }> {
    const current = this.ctx.storage.sql
      .exec(
        `
          SELECT owner, token, expires_at_ms
          FROM leases
          WHERE lane = ?
        `,
        input.lane,
      )
      .toArray()[0] as unknown as LeaseRow | undefined;

    if (!current || current.token !== input.token) {
      return {
        released: false,
        lane: input.lane,
      };
    }

    this.ctx.storage.sql.exec(
      `
        DELETE FROM leases
        WHERE lane = ? AND token = ?
      `,
      input.lane,
      input.token,
    );

    return {
      released: true,
      lane: input.lane,
    };
  }

  async status(): Promise<CoordinatorStatus> {
    const now = Date.now();

    this.ctx.storage.sql.exec(
      "DELETE FROM deliveries WHERE expires_at_ms <= ?",
      now,
    );

    this.ctx.storage.sql.exec(
      "DELETE FROM leases WHERE expires_at_ms <= ?",
      now,
    );

    const deliveryRow = this.ctx.storage.sql
      .exec("SELECT COUNT(*) AS count FROM deliveries")
      .toArray()[0] as { count: number } | undefined;

    const leaseRow = this.ctx.storage.sql
      .exec("SELECT COUNT(*) AS count FROM leases")
      .toArray()[0] as { count: number } | undefined;

    return {
      deliveryCount: Number(deliveryRow?.count || 0),
      activeLeaseCount: Number(leaseRow?.count || 0),
      now: iso(now),
    };
  }
}

export default {
  async fetch(request, env): Promise<Response> {
    const url = new URL(request.url);

    if (request.method === "GET" && url.pathname === "/health") {
      return jsonResponse({
        ok: true,
        service: SERVICE_NAME,
        mode: "INTERNAL_SERVICE",
        storage: "DURABLE_OBJECT_SQLITE",
      });
    }

    if (request.method !== "POST") {
      return new Response("Method Not Allowed", {
        status: 405,
        headers: {
          Allow: "GET, POST",
        },
      });
    }

    let body: JsonObject;

    try {
      body = await readJsonObject(request);
    } catch (error) {
      return jsonResponse(
        {
          ok: false,
          error:
            error instanceof Error
              ? error.message
              : "invalid request",
        },
        400,
      );
    }

    let repository: string;

    try {
      repository = requireString(
        body,
        "repository",
        REPOSITORY_RE,
      );
    } catch (error) {
      return jsonResponse(
        {
          ok: false,
          error:
            error instanceof Error
              ? error.message
              : "invalid repository",
        },
        400,
      );
    }

    const stub = env.CONTROL_PLANE_COORDINATOR.getByName(
      normalizeRepository(repository),
    );

    try {
      if (url.pathname === "/v1/delivery/register") {
        const delivery = requireString(
          body,
          "delivery",
          DELIVERY_RE,
        );

        const event = requireString(body, "event");
        const action = optionalString(body, "action", 120);
        const headSha = optionalString(body, "headSha", 80);
        const prNumber = optionalNullableInteger(body, "prNumber");
        const issueNumber = optionalNullableInteger(
          body,
          "issueNumber",
        );
        const retentionSeconds = optionalInteger(
          body,
          "retentionSeconds",
        );

        const result = await stub.registerDelivery({
          delivery,
          event,
          action,
          prNumber,
          issueNumber,
          headSha,
          retentionSeconds,
        });

        return jsonResponse({
          ok: true,
          repository,
          ...result,
        });
      }

      if (url.pathname === "/v1/lease/acquire") {
        const lane = requireString(body, "lane", LANE_RE);
        const owner = requireString(body, "owner", OWNER_RE);
        const ttlSeconds = optionalInteger(
          body,
          "ttlSeconds",
        );

        const result = await stub.acquireLease({
          lane,
          owner,
          ttlSeconds,
        });

        return jsonResponse({
          ok: true,
          repository,
          ...result,
        });
      }

      if (url.pathname === "/v1/lease/renew") {
        const lane = requireString(body, "lane", LANE_RE);
        const token = requireString(
          body,
          "token",
          DELIVERY_RE,
        );
        const ttlSeconds = optionalInteger(
          body,
          "ttlSeconds",
        );

        const result = await stub.renewLease({
          lane,
          token,
          ttlSeconds,
        });

        return jsonResponse({
          ok: true,
          repository,
          ...result,
        });
      }

      if (url.pathname === "/v1/lease/release") {
        const lane = requireString(body, "lane", LANE_RE);
        const token = requireString(
          body,
          "token",
          DELIVERY_RE,
        );

        const result = await stub.releaseLease({
          lane,
          token,
        });

        return jsonResponse({
          ok: true,
          repository,
          ...result,
        });
      }

      if (url.pathname === "/v1/status") {
        const result = await stub.status();

        return jsonResponse({
          ok: true,
          repository,
          ...result,
        });
      }

      return new Response("Not Found", {
        status: 404,
      });
    } catch (error) {
      console.error(
        JSON.stringify({
          type: "coordinator_error",
          path: url.pathname,
          repository,
          error:
            error instanceof Error
              ? error.message
              : "unknown error",
        }),
      );

      return jsonResponse(
        {
          ok: false,
          error: "coordinator operation failed",
        },
        500,
      );
    }
  },
} satisfies ExportedHandler<Env>;
