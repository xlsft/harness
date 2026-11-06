/**
 * The webhook of a scheduled task. A schedule runs on one of the user's machines, which may be asleep, so a post becomes a
 * DOCUMENT_KIND_SCHEDULE_TRIGGER document in the user's sync feed: that machine runs it when it next sees the feed and
 * deletes the document. The URL holds the schedule document's id and a secret; the server only has the secret's hash,
 * from the schedule document itself.
 */
import { createHash, randomUUID, timingSafeEqual } from "node:crypto";
import { and, count, eq, isNull } from "drizzle-orm";
import { getDb, schema } from "../db/index.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { TokenBucketLimiter } from "../lib/rate-limit.ts";
import { putDocument } from "./sync.ts";

/** What a post may carry, and how many may wait for a machine that is away. */
export const MAX_WEBHOOK_BODY = 64 << 10;
const MAX_PENDING = 100;

// A burst of 30, then one every two seconds, per schedule: what a run costs is the user's.
const limiter = new TokenBucketLimiter(30, 0.5);

export interface WebhookResult {
  status: number;
  body: Record<string, unknown>;
  retryAfterMs?: number;
}

function secretMatches(secret: string, wantHex: unknown): boolean {
  if (typeof wantHex !== "string" || !/^[0-9a-f]{64}$/.test(wantHex)) return false;
  return timingSafeEqual(createHash("sha256").update(secret).digest(), Buffer.from(wantHex, "hex"));
}

/** Handles one post. Every way a token can be wrong reads the same from outside. */
export async function receiveScheduleWebhook(token: string, payload: string): Promise<WebhookResult> {
  const notFound: WebhookResult = { status: 404, body: { error: "not found" } };
  const dot = token.indexOf(".");
  if (dot <= 0) return notFound;
  const id = token.slice(0, dot);
  const secret = token.slice(dot + 1);
  const d = schema.syncDocument;
  const [row] = await getDb()
    .select()
    .from(d)
    .where(and(eq(d.id, id), eq(d.kind, DocumentKind.SCHEDULE), eq(d.scope, Scope.USER), isNull(d.deletedAt)));
  if (!row) return notFound;
  let task: { deviceId?: unknown; webhookSecretSha256?: unknown; paused?: unknown; done?: unknown; id?: unknown; trigger?: { kind?: unknown } };
  try {
    task = JSON.parse(row.content);
  } catch {
    return notFound;
  }
  if (task.trigger?.kind !== "webhook" || !secretMatches(secret, task.webhookSecretSha256)) return notFound;
  if (task.paused === true || task.done === true) return { status: 409, body: { error: "the schedule is not running" } };
  const wait = limiter.take(row.id);
  if (wait > 0) return { status: 429, body: { error: "too many posts" }, retryAfterMs: wait };
  const [pending] = await getDb()
    .select({ n: count() })
    .from(d)
    .where(and(eq(d.ownerId, row.ownerId), eq(d.kind, DocumentKind.SCHEDULE_TRIGGER), isNull(d.deletedAt)));
  if ((pending?.n ?? 0) >= MAX_PENDING) return { status: 429, body: { error: "too many posts are waiting for the machine that runs this" } };

  const content = JSON.stringify({ scheduleId: row.key, deviceId: task.deviceId ?? "", source: "webhook", receivedAt: new Date().toISOString(), payload });
  await putDocument(
    { user: { id: row.ownerId, email: "", name: "", role: "user" }, via: "session" },
    { ref: { scope: Scope.USER, kind: DocumentKind.SCHEDULE_TRIGGER, key: `trg_${randomUUID()}`, projectKey: "" }, content, files: [], targetRoles: [], source: "webhook" },
  );
  return { status: 202, body: { accepted: true } };
}
