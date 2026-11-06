/**
 * The general audit log. record() appends an event; it never fails the operation being audited (an error is logged). A
 * failing audit write must not lock people out of the server, and the forwarders (forward.ts) read the same table, so an
 * event is either stored and forwarded or logged as lost.
 *
 * Secrets stay out in two layers: callers put only named, non-secret fields into `details`, and scrub() removes whatever
 * looks like a credential by key or by value, however deep, on the way in.
 */
import { and, desc, eq, gte, ilike, lt, or, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { countMetric } from "../telemetry/metrics.ts";
import { kickForwarders } from "./forward.ts";

export interface AuditInput {
  action: string;
  outcome?: "success" | "failure";
  actor?: { id?: string; label?: string; ip?: string };
  target?: { type?: string; id?: string; label?: string };
  details?: Record<string, unknown>;
}

export interface AuditRow {
  id: number;
  at: string;
  action: string;
  outcome: string;
  actor_id: string;
  actor: string;
  actor_ip: string;
  target_type: string;
  target_id: string;
  target: string;
  details: Record<string, unknown>;
}

const SECRET_KEY = /pass(word|wd|phrase)?|secret|token|api[-_]?key|credential|authorization|cookie|private[-_]?key|signature|bearer|recovery|otp|totp|^code$|^key$/i;
/** Values that are credentials whatever key they sit under: our own tokens, JWTs, PEM keys, common vendor key shapes. */
const SECRET_VALUE = /^(hat_|hdt_|hdc_|hsc_|sk-|xox[a-z]-|gh[pousr]_|glpat-)|^eyJ[\w-]+\.[\w-]+\.|-----BEGIN [A-Z ]*PRIVATE KEY-----/;
const MAX_DEPTH = 6;
const MAX_STRING = 500;
const MAX_ITEMS = 100;

/** A copy with credential-looking keys and values removed and sizes bounded. */
export function scrub(value: unknown, depth = 0): unknown {
  if (depth > MAX_DEPTH) return "[deep]";
  if (typeof value === "string") {
    if (SECRET_VALUE.test(value)) return "[redacted]";
    return value.length > MAX_STRING ? `${value.slice(0, MAX_STRING)}…` : value;
  }
  if (typeof value === "number" || typeof value === "boolean" || value === null || value === undefined) return value;
  if (typeof value === "bigint") return value.toString();
  if (Array.isArray(value)) return value.slice(0, MAX_ITEMS).map((v) => scrub(v, depth + 1));
  if (value instanceof Date) return value.toISOString();
  if (typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>).slice(0, MAX_ITEMS)) {
      // "secret_set: true" says a secret exists and "*_at"/"*_count" are not values of one: booleans and numbers stay.
      out[k] = SECRET_KEY.test(k) && typeof v !== "boolean" && typeof v !== "number" ? "[redacted]" : scrub(v, depth + 1);
    }
    return out;
  }
  return String(value);
}

const clip = (s: string | undefined, n = 300) => (s ?? "").slice(0, n);

export async function record(e: AuditInput): Promise<void> {
  const category = e.action.split(".")[0] ?? "";
  try {
    await ensureDb();
    await getDb()
      .insert(schema.auditEvent)
      .values({
        at: new Date(),
        action: e.action,
        category,
        outcome: e.outcome ?? "success",
        actorId: clip(e.actor?.id),
        actorLabel: clip(e.actor?.label),
        actorIp: clip(e.actor?.ip, 64),
        targetType: clip(e.target?.type, 64),
        targetId: clip(e.target?.id),
        targetLabel: clip(e.target?.label),
        details: (scrub(e.details ?? {}) as Record<string, unknown>) ?? {},
      });
    countMetric("harness.audit.events", 1, { category, outcome: e.outcome ?? "success" });
    kickForwarders();
  } catch (err) {
    console.error(`[harness] could not write audit event ${e.action}: ${err instanceof Error ? err.message : String(err)}`);
  }
}

export interface AuditFilter {
  action?: string;
  actor?: string;
  target?: string;
  outcome?: string;
  from?: string;
  to?: string;
}

function time(v: string | undefined, field: string): Date | undefined {
  if (!v) return undefined;
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) throw new ApiError("invalid_request", `${field} must be an RFC 3339 timestamp`);
  return d;
}

const like = (s: string) => `%${s.replace(/[\\%_]/g, (c) => `\\${c}`)}%`;

function whereOf(f: AuditFilter, beforeId?: number): SQL | undefined {
  const a = schema.auditEvent;
  const conds: (SQL | undefined)[] = [];
  const action = f.action?.trim();
  if (action) conds.push(action.includes(".") ? eq(a.action, action) : eq(a.category, action));
  const actor = f.actor?.trim();
  if (actor) conds.push(or(ilike(a.actorId, like(actor)), ilike(a.actorLabel, like(actor))));
  const target = f.target?.trim();
  if (target) conds.push(or(ilike(a.targetId, like(target)), ilike(a.targetLabel, like(target))));
  if (f.outcome) {
    if (f.outcome !== "success" && f.outcome !== "failure") throw new ApiError("invalid_request", "outcome must be success or failure");
    conds.push(eq(a.outcome, f.outcome));
  }
  const from = time(f.from, "from");
  if (from) conds.push(gte(a.at, from));
  const to = time(f.to, "to");
  if (to) conds.push(lt(a.at, to));
  if (beforeId !== undefined) conds.push(lt(a.id, beforeId));
  return conds.length ? and(...conds) : undefined;
}

export function toAuditRow(r: typeof schema.auditEvent.$inferSelect): AuditRow {
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

export async function listAudit(f: AuditFilter, limit = 100, pageToken = ""): Promise<{ events: AuditRow[]; next_page_token: string }> {
  await ensureDb();
  const n = Math.min(Math.max(limit > 0 ? limit : 100, 1), 500);
  const before = pageToken ? Number(pageToken) : undefined;
  if (before !== undefined && !Number.isSafeInteger(before)) throw new ApiError("invalid_request", "Invalid page token");
  const rows = await getDb()
    .select()
    .from(schema.auditEvent)
    .where(whereOf(f, before))
    .orderBy(desc(schema.auditEvent.id))
    .limit(n + 1);
  const page = rows.slice(0, n);
  return { events: page.map(toAuditRow), next_page_token: rows.length > n ? String(page[page.length - 1]!.id) : "" };
}

/** A CSV cell. Quoted as needed, and defused: a cell a spreadsheet would run as a formula gets a leading apostrophe. */
export function csvCell(v: unknown): string {
  let s = v === null || v === undefined ? "" : String(v);
  if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`;
  return /[",\r\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s;
}

export function csvRow(cells: unknown[]): string {
  return `${cells.map(csvCell).join(",")}\r\n`;
}

/** Row cap of the CSV exports: their responses go through the client's proxy, which bounds them. */
export const CSV_MAX_ROWS = 50_000;
export const CSV_MAX_BYTES = 12 * 1024 * 1024;

export async function auditCsv(f: AuditFilter): Promise<{ csv: string; rows: number; truncated: boolean }> {
  await ensureDb();
  let out = csvRow(["time", "action", "outcome", "actor_id", "actor", "actor_ip", "target_type", "target_id", "target", "details"]);
  let rows = 0;
  let before: number | undefined;
  for (;;) {
    const page = await getDb().select().from(schema.auditEvent).where(whereOf(f, before)).orderBy(desc(schema.auditEvent.id)).limit(1000);
    for (const r of page) {
      const a = toAuditRow(r);
      const line = csvRow([a.at, a.action, a.outcome, a.actor_id, a.actor, a.actor_ip, a.target_type, a.target_id, a.target, JSON.stringify(a.details)]);
      if (rows >= CSV_MAX_ROWS || out.length + line.length > CSV_MAX_BYTES) return { csv: out, rows, truncated: true };
      out += line;
      rows++;
    }
    if (page.length < 1000) return { csv: out, rows, truncated: false };
    before = page[page.length - 1]!.id;
  }
}

/** Deletes events older than HARNESS_AUDIT_RETENTION_DAYS (unset or 0: keep them). */
export async function purgeAudit(now = Date.now()): Promise<void> {
  const days = Number(process.env.HARNESS_AUDIT_RETENTION_DAYS ?? 0);
  if (!Number.isFinite(days) || days <= 0) return;
  await ensureDb();
  await getDb().delete(schema.auditEvent).where(lt(schema.auditEvent.at, new Date(now - days * 86_400_000)));
}
