/**
 * Session sync (proto/harness/sync/v1/sync.proto, part 2): metadata + durable transcript.
 * Transcript entries are stored byte-exact, deduplicated by uuid, with a server-assigned seq.
 */
import { clone, fromJson, toJson, type JsonValue } from "@bufbuild/protobuf";
import { and, asc, desc, eq, exists, gt, inArray, lt, or, sql, type AnyColumn, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { SessionInfoSchema, type SessionInfo, type SessionStatus } from "../gen/harness/agent/v1/agent_pb.ts";
import { Mutex } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal } from "./devices.ts";

export type SessionRow = typeof schema.syncSession.$inferSelect;
export type EntryRow = typeof schema.syncSessionEntry.$inferSelect;

export interface SessionView {
  info: SessionInfo;
  promptBundleVersion: string;
  lastSeq: number;
  createdAt: string;
  lastActivityAt: string;
}

export interface EntryInput {
  uuid: string;
  parentUuid: string;
  type: string;
  timestamp: string;
  entryJson: string;
  /** Subagent id; "" for the main thread. */
  agentId: string;
}

export const MAX_ENTRIES = 500;
export const MAX_APPEND_BYTES = 4 << 20;
const SEARCH_TEXT_MAX = 16_384;
/** Session ids are UUID v4 (they double as the local JSONL file name). */
const SESSION_ID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

const sessionMutex = new Mutex();

export function sessionView(row: SessionRow): SessionView {
  return {
    info: fromJson(SessionInfoSchema, row.info as JsonValue, { ignoreUnknownFields: true }),
    promptBundleVersion: row.promptBundleVersion,
    lastSeq: row.lastSeq,
    createdAt: row.createdAt.toISOString(),
    lastActivityAt: row.lastActivityAt.toISOString(),
  };
}

/** Resolves an update path ("info.title", "prompt_bundle_version", "info") to what it changes. */
function resolvePath(path: string): { kind: "info" } | { kind: "field"; localName: string } | { kind: "bundle" } | { kind: "ignored" } {
  const p = path.trim();
  if (p === "prompt_bundle_version" || p === "promptBundleVersion") return { kind: "bundle" };
  if (p === "info") return { kind: "info" };
  if (["last_seq", "lastSeq", "created_at", "createdAt", "last_activity_at", "lastActivityAt"].includes(p)) return { kind: "ignored" };
  // SessionInfo fields always carry the "info." prefix.
  if (!p.startsWith("info.")) throw new ApiError("invalid_request", `Unknown update path ${path} (SessionInfo fields use the "info." prefix)`);
  const name = p.slice(5);
  const f = SessionInfoSchema.fields.find((x) => x.name === name || x.localName === name || x.jsonName === name);
  if (!f) throw new ApiError("invalid_request", `Unknown update path ${path}`);
  if (f.localName === "id") return { kind: "ignored" };
  return { kind: "field", localName: f.localName };
}

async function ownedSession(p: Principal, id: string): Promise<SessionRow> {
  const row = await getDb().query.syncSession.findFirst({ where: eq(schema.syncSession.id, id) });
  if (!row || row.userId !== p.user.id) throw new ApiError("not_found", `Session ${id} not found`);
  return row;
}

export async function upsertSession(
  p: Principal,
  input: { info?: SessionInfo; promptBundleVersion: string },
  updatePaths: string[],
): Promise<SessionView> {
  const incoming = input.info;
  if (!incoming?.id) throw new ApiError("invalid_request", "session.info.id is required");
  if (!SESSION_ID_RE.test(incoming.id)) throw new ApiError("invalid_request", "Session id must be a UUID v4");
  const resolved = updatePaths.map(resolvePath);
  await ensureDb();

  return sessionMutex.run(async () => {
    const db = getDb();
    const s = schema.syncSession;
    const cur = await db.query.syncSession.findFirst({ where: eq(s.id, incoming.id) });
    if (cur && cur.userId !== p.user.id) throw new ApiError("permission", "The session belongs to another user");

    let info: SessionInfo;
    let bundle = cur?.promptBundleVersion ?? "";
    if (!cur || resolved.length === 0) {
      info = clone(SessionInfoSchema, incoming);
      bundle = input.promptBundleVersion;
    } else {
      info = sessionView(cur).info;
      for (const r of resolved) {
        if (r.kind === "bundle") bundle = input.promptBundleVersion;
        else if (r.kind === "info") info = clone(SessionInfoSchema, incoming);
        else if (r.kind === "field") (info as unknown as Record<string, unknown>)[r.localName] = (incoming as unknown as Record<string, unknown>)[r.localName];
      }
    }
    info.id = incoming.id;
    const now = new Date();
    if (!incoming.updatedAt || (cur && resolved.length && !resolved.some((r) => r.kind === "info" || (r.kind === "field" && r.localName === "updatedAt")))) {
      info.updatedAt = now.toISOString();
    }
    if (!cur && !info.deviceId && p.deviceId) {
      info.deviceId = p.deviceId;
      const dev = await db.query.device.findFirst({ where: eq(schema.device.id, p.deviceId) });
      if (dev && !info.deviceName) info.deviceName = dev.name;
    }
    const cols = {
      info: toJson(SessionInfoSchema, info) as object,
      title: info.title,
      cwd: info.cwd,
      repo: info.repo,
      deviceId: info.deviceId,
      status: info.status,
      archived: info.archived,
      promptBundleVersion: bundle,
    };
    let row: SessionRow;
    if (cur) {
      [row] = (await db.update(s).set(cols).where(eq(s.id, cur.id)).returning()) as [SessionRow];
    } else {
      [row] = (await db
        .insert(s)
        .values({ id: incoming.id, userId: p.user.id, lastSeq: 0, createdAt: now, lastActivityAt: now, ...cols })
        .returning()) as [SessionRow];
    }
    return sessionView(row);
  });
}

/** Text worth searching in one CC-format JSONL entry: message text blocks and summaries. */
export function extractSearchText(entryJson: string): string {
  let v: any;
  try {
    v = JSON.parse(entryJson);
  } catch {
    return "";
  }
  const parts: string[] = [];
  const push = (s: unknown) => {
    if (typeof s === "string" && s) parts.push(s);
  };
  if (v && typeof v === "object") {
    push(v.summary);
    const content = v.message?.content;
    if (typeof content === "string") push(content);
    else if (Array.isArray(content)) {
      for (const b of content) {
        if (b?.type === "text") push(b.text);
        else if (b?.type === "tool_result" && typeof b.content === "string") push(b.content);
      }
    }
  }
  return parts.join("\n").slice(0, SEARCH_TEXT_MAX);
}

export async function appendSessionEvents(p: Principal, sessionId: string, entries: EntryInput[]) {
  if (!sessionId) throw new ApiError("invalid_request", "session_id is required");
  if (entries.length > MAX_ENTRIES) throw new ApiError("invalid_request", `At most ${MAX_ENTRIES} entries per call`);
  let bytes = 0;
  for (const e of entries) {
    if (!e.uuid || e.uuid.length > 128) throw new ApiError("invalid_request", "Every entry needs a uuid (max 128 chars)");
    if (!e.entryJson) throw new ApiError("invalid_request", `Entry ${e.uuid} has no entry_json`);
    if ((e.agentId ?? "").length > 128) throw new ApiError("invalid_request", "agent_id is too long");
    bytes += Buffer.byteLength(e.entryJson, "utf8");
  }
  if (bytes > MAX_APPEND_BYTES) throw new ApiError("invalid_request", `Entries exceed ${MAX_APPEND_BYTES} bytes per call`);
  await ensureDb();

  return sessionMutex.run(async () => {
    await ownedSession(p, sessionId);
    return getDb().transaction(async (tx) => {
      const s = schema.syncSession;
      const e = schema.syncSessionEntry;
      const cur = (await tx.select().from(s).where(eq(s.id, sessionId)).get())!;
      // Dedup key: (agent_id, uuid) within the session.
      const keyOf = (agentId: string, uuid: string) => JSON.stringify([agentId, uuid]);
      const uuids = [...new Set(entries.map((x) => x.uuid))];
      const existing = new Set<string>();
      for (let i = 0; i < uuids.length; i += 200) {
        const chunk = uuids.slice(i, i + 200);
        const rows = await tx
          .select({ uuid: e.uuid, agentId: e.agentId })
          .from(e)
          .where(and(eq(e.sessionId, sessionId), inArray(e.uuid, chunk)));
        for (const r of rows) existing.add(keyOf(r.agentId, r.uuid));
      }
      let seq = cur.lastSeq;
      let accepted = 0;
      let duplicates = 0;
      const values: (typeof e.$inferInsert)[] = [];
      for (const x of entries) {
        const k = keyOf(x.agentId ?? "", x.uuid);
        if (existing.has(k)) {
          duplicates++;
          continue;
        }
        existing.add(k);
        seq++;
        accepted++;
        values.push({
          sessionId,
          seq,
          uuid: x.uuid,
          agentId: x.agentId ?? "",
          parentUuid: x.parentUuid ?? "",
          type: x.type ?? "",
          timestamp: x.timestamp ?? "",
          entryJson: x.entryJson,
          searchText: extractSearchText(x.entryJson),
        });
      }
      for (let i = 0; i < values.length; i += 50) await tx.insert(e).values(values.slice(i, i + 50));
      if (accepted) await tx.update(s).set({ lastSeq: seq, lastActivityAt: new Date() }).where(eq(s.id, sessionId));
      return { accepted, duplicates, lastSeq: seq };
    });
  });
}

export async function getSession(p: Principal, sessionId: string): Promise<SessionView> {
  if (!sessionId) throw new ApiError("invalid_request", "session_id is required");
  await ensureDb();
  return sessionView(await ownedSession(p, sessionId));
}

export interface ListSessionsInput {
  repo?: string;
  cwd?: string;
  deviceId?: string;
  statuses?: SessionStatus[];
  query?: string;
  includeArchived?: boolean;
  pageSize?: number;
  pageToken?: string;
}

function likePattern(q: string): string {
  return `%${q.toLowerCase().replace(/[\\%_]/g, (c) => `\\${c}`)}%`;
}

function decodeToken(token: string): { t: number; id: string } {
  try {
    const v = JSON.parse(Buffer.from(token, "base64url").toString("utf8"));
    if (typeof v.t === "number" && typeof v.id === "string") return v;
  } catch {
    // fall through
  }
  throw new ApiError("invalid_request", "Invalid page_token");
}

export async function listSessions(p: Principal, f: ListSessionsInput): Promise<{ sessions: SessionView[]; nextPageToken: string }> {
  await ensureDb();
  const s = schema.syncSession;
  const e = schema.syncSessionEntry;
  const size = Math.min(Math.max(f.pageSize || 50, 1), 200);
  const conds: (SQL | undefined)[] = [eq(s.userId, p.user.id)];
  if (f.repo) conds.push(eq(s.repo, f.repo));
  if (f.cwd) conds.push(eq(s.cwd, f.cwd));
  if (f.deviceId) conds.push(eq(s.deviceId, f.deviceId));
  if (f.statuses?.length) conds.push(inArray(s.status, f.statuses));
  if (!f.includeArchived) conds.push(eq(s.archived, false));
  const q = f.query?.trim();
  if (q) {
    // Portable case-insensitive substring search (LIKE ... ESCAPE works on SQLite and Postgres).
    const pat = likePattern(q);
    const like = (col: AnyColumn) => sql`lower(${col}) like ${pat} escape '\\'`;
    conds.push(
      or(
        like(s.title),
        like(s.cwd),
        like(s.repo),
        exists(
          getDb()
            .select({ one: sql`1` })
            .from(e)
            .where(and(eq(e.sessionId, s.id), like(e.searchText))),
        ),
      ),
    );
  }
  if (f.pageToken) {
    const t = decodeToken(f.pageToken);
    const at = new Date(t.t);
    conds.push(or(lt(s.lastActivityAt, at), and(eq(s.lastActivityAt, at), lt(s.id, t.id))));
  }
  const rows = await getDb()
    .select()
    .from(s)
    .where(and(...conds))
    .orderBy(desc(s.lastActivityAt), desc(s.id))
    .limit(size + 1);
  const page = rows.slice(0, size);
  const last = page.at(-1);
  return {
    sessions: page.map(sessionView),
    nextPageToken: rows.length > size && last ? Buffer.from(JSON.stringify({ t: last.lastActivityAt.getTime(), id: last.id })).toString("base64url") : "",
  };
}

/** Entries of one thread (the main thread for agentId "", otherwise that subagent), in seq order. */
export async function getSessionTranscript(p: Principal, sessionId: string, afterSeq: number, pageSize?: number, agentId = "") {
  if (!sessionId) throw new ApiError("invalid_request", "session_id is required");
  await ensureDb();
  await ownedSession(p, sessionId);
  const e = schema.syncSessionEntry;
  const size = Math.min(Math.max(pageSize || 500, 1), 2000);
  const rows = await getDb()
    .select()
    .from(e)
    .where(and(eq(e.sessionId, sessionId), eq(e.agentId, agentId), gt(e.seq, Math.max(0, Math.floor(afterSeq)))))
    .orderBy(asc(e.seq))
    .limit(size + 1);
  return { entries: rows.slice(0, size), hasMore: rows.length > size };
}


