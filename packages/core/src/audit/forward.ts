/**
 * Forwards audit events to a SIEM, configured by environment:
 *   HARNESS_AUDIT_WEBHOOK_URL     POST a JSON array of events (HARNESS_AUDIT_WEBHOOK_SECRET signs the body with
 *                                 HMAC-SHA256: x-harness-signature: sha256=<hex>)
 *   HARNESS_AUDIT_SYSLOG_URL      udp://host:514, tcp://host:601 or tls://host:6514: RFC 5424 messages, one per event
 *
 * It reads from the audit table with a stored cursor per sink rather than queueing in memory, so events written while a
 * collector is down or the server restarts are delivered afterwards (at least once; a consumer dedupes by id). The first
 * run starts at the current end of the log: turning a sink on does not replay history.
 */
import { createHmac } from "node:crypto";
import { createSocket } from "node:dgram";
import { connect as netConnect } from "node:net";
import { connect as tlsConnect } from "node:tls";
import { asc, desc, eq, gt } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import type { AuditRow } from "./log.ts";

const BATCH = 200;
const RETRY_MS = 30_000;

interface Sink {
  name: string;
  send(events: AuditRow[]): Promise<void>;
}

function toRow(r: typeof schema.auditEvent.$inferSelect): AuditRow {
  return {
    id: r.id,
    at: r.at.toISOString(),
    action: r.action,
    outcome: r.outcome,
    actor_id: r.actorId,
    actor: r.actorLabel,
    actor_ip: r.actorIp,
    target_type: r.targetType,
    target_id: r.targetId,
    target: r.targetLabel,
    details: (r.details as Record<string, unknown>) ?? {},
  };
}

function webhookSink(url: string, secret: string | undefined): Sink {
  return {
    name: "webhook",
    async send(events) {
      const body = JSON.stringify(events);
      const headers: Record<string, string> = { "content-type": "application/json" };
      if (secret) headers["x-harness-signature"] = `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`;
      const res = await fetch(url, { method: "POST", headers, body, signal: AbortSignal.timeout(15_000), redirect: "error" });
      await res.body?.cancel();
      if (!res.ok) throw new Error(`webhook answered ${res.status}`);
    },
  };
}

/** Structured data values: RFC 5424 section 6.3.3 escapes. */
const sdValue = (s: string) => s.replace(/["\\\]]/g, (c) => `\\${c}`);

/** One event as an RFC 5424 message: facility local0 (16), severity notice (5) or warning (4) for failures. */
export function syslogMessage(e: AuditRow, hostname = "harness"): string {
  const pri = 16 * 8 + (e.outcome === "failure" ? 4 : 5);
  const sd = [
    ["id", String(e.id)],
    ["action", e.action],
    ["outcome", e.outcome],
    ["actorId", e.actor_id],
    ["actor", e.actor],
    ["ip", e.actor_ip],
    ["targetType", e.target_type],
    ["targetId", e.target_id],
    ["target", e.target],
  ]
    .filter(([, v]) => v)
    .map(([k, v]) => `${k}="${sdValue(v!)}"`)
    .join(" ");
  return `<${pri}>1 ${e.at} ${hostname} harness - ${e.action} [audit@32473 ${sd}] ${JSON.stringify(e.details)}`;
}

function syslogSink(raw: string): Sink {
  const url = new URL(raw);
  const port = Number(url.port) || (url.protocol === "tls:" ? 6514 : url.protocol === "tcp:" ? 601 : 514);
  const host = url.hostname.replace(/^\[|\]$/g, "");
  if (url.protocol === "udp:") {
    return {
      name: "syslog",
      async send(events) {
        const socket = createSocket(host.includes(":") ? "udp6" : "udp4");
        try {
          for (const e of events) {
            const msg = Buffer.from(syslogMessage(e));
            await new Promise<void>((resolve, reject) => socket.send(msg, port, host, (err) => (err ? reject(err) : resolve())));
          }
        } finally {
          socket.close();
        }
      },
    };
  }
  if (url.protocol !== "tcp:" && url.protocol !== "tls:") throw new Error(`HARNESS_AUDIT_SYSLOG_URL: use udp://, tcp:// or tls:// (got ${url.protocol})`);
  return {
    name: "syslog",
    send(events) {
      // RFC 6587 octet counting: survives messages with newlines.
      const payload = events.map((e) => {
        const m = Buffer.from(syslogMessage(e));
        return Buffer.concat([Buffer.from(`${m.length} `), m]);
      });
      return new Promise<void>((resolve, reject) => {
        const socket = url.protocol === "tls:" ? tlsConnect({ host, port, servername: host }) : netConnect({ host, port });
        socket.setTimeout(15_000, () => socket.destroy(new Error("syslog connection timed out")));
        socket.once("error", reject);
        socket.once(url.protocol === "tls:" ? "secureConnect" : "connect", () => {
          socket.end(Buffer.concat(payload), () => resolve());
        });
      });
    },
  };
}

function sinks(): Sink[] {
  const out: Sink[] = [];
  const hook = process.env.HARNESS_AUDIT_WEBHOOK_URL?.trim();
  if (hook) out.push(webhookSink(hook, process.env.HARNESS_AUDIT_WEBHOOK_SECRET));
  const sys = process.env.HARNESS_AUDIT_SYSLOG_URL?.trim();
  if (sys) out.push(syslogSink(sys));
  return out;
}

const cursorKey = (sink: string) => `audit_forward_cursor:${sink}`;

async function readCursor(sink: string): Promise<number> {
  const db = getDb();
  const r = await db.query.setting.findFirst({ where: eq(schema.setting.key, cursorKey(sink)) });
  if (r) return Number(r.value) || 0;
  // Starts at the current end, one event back: the event that woke the forwarder is the first one sent.
  const [last] = await db.select({ id: schema.auditEvent.id }).from(schema.auditEvent).orderBy(desc(schema.auditEvent.id)).limit(1);
  const start = Math.max((last?.id ?? 1) - 1, 0);
  await writeCursor(sink, start);
  return start;
}

async function writeCursor(sink: string, id: number): Promise<void> {
  const now = new Date();
  await getDb()
    .insert(schema.setting)
    .values({ key: cursorKey(sink), value: id, updatedAt: now })
    .onConflictDoUpdate({ target: schema.setting.key, set: { value: id, updatedAt: now } });
}

async function drain(sink: Sink): Promise<void> {
  await ensureDb();
  let cursor = await readCursor(sink.name);
  for (;;) {
    const rows = await getDb().select().from(schema.auditEvent).where(gt(schema.auditEvent.id, cursor)).orderBy(asc(schema.auditEvent.id)).limit(BATCH);
    if (!rows.length) return;
    await sink.send(rows.map(toRow));
    cursor = rows[rows.length - 1]!.id;
    await writeCursor(sink.name, cursor);
    if (rows.length < BATCH) return;
  }
}

let running: Promise<void> | undefined;
let again = false;
let retryTimer: NodeJS.Timeout | undefined;

/** Delivers what is pending to every sink. A failing sink keeps its cursor and is retried later. */
async function pump(): Promise<void> {
  do {
    again = false;
    for (const sink of sinks()) {
      try {
        await drain(sink);
      } catch (err) {
        console.error(`[harness] audit forward to ${sink.name} failed, will retry: ${err instanceof Error ? err.message : String(err)}`);
        clearTimeout(retryTimer);
        retryTimer = setTimeout(kickForwarders, RETRY_MS);
        retryTimer.unref();
      }
    }
  } while (again);
}

/** After a new event. Never throws, and cheap when no sink is configured. */
export function kickForwarders(): void {
  if (!process.env.HARNESS_AUDIT_WEBHOOK_URL && !process.env.HARNESS_AUDIT_SYSLOG_URL) return;
  if (running) {
    again = true;
    return;
  }
  running = pump().finally(() => {
    running = undefined;
  });
}

/** Resolves once everything written so far was attempted (tests, shutdown). */
export async function flushForwarders(): Promise<void> {
  kickForwarders();
  while (running) await running;
}
