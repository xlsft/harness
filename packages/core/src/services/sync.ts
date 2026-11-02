/**
 * Sync documents (proto/harness/sync/v1/sync.proto, part 1). Transport-agnostic.
 *
 * - Every write takes the next value of one global, strictly increasing counter (nextSyncVersion);
 *   a document's `version` is the counter value of its last write and the CAS token.
 * - Writes are serialized in-process (writeMutex) and each runs in a DB transaction (counter + row +
 *   revision), so versions are committed in order and live events are published in version order.
 *   The counter row lock and SELECT ... FOR UPDATE on the document also keep CAS correct if several
 *   processes ever share one database (live events would then only reach the writer's own subscribers).
 * - Deletes keep the row as a tombstone (deleted_at). Tombstones older than the retention (>= 30 days)
 *   are purged; the highest purged version is remembered and cursors older than it get resync_required.
 * - Visibility: a caller's feed is its own user/project documents plus org documents. Org documents
 *   targeted at roles the caller does not have appear as tombstones, so a cached copy is dropped when a
 *   document is re-targeted away from the caller's role (only kind + key are revealed).
 */
import { createHash } from "node:crypto";
import { and, asc, eq, gt, inArray, isNotNull, lt, lte, max, or, sql } from "drizzle-orm";
import { ensureDb, getDb, schema, type Db } from "../db/index.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { Mutex } from "../lib/async.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal, PublicUser } from "./devices.ts";

export type DocRow = typeof schema.syncDocument.$inferSelect;
type Tx = Parameters<Parameters<Db["transaction"]>[0]>[0];

export interface StoredFile {
  path: string;
  /** base64 */
  data: string;
  executable: boolean;
}

export interface FileInput {
  path: string;
  content: Uint8Array;
  executable: boolean;
}

export interface DocRef {
  scope: Scope;
  kind: DocumentKind;
  key: string;
  projectKey: string;
}

export interface PutInput {
  ref: DocRef;
  content: string;
  files: FileInput[];
  /** undefined: unconditional; 0: create only; N: CAS on version N. */
  baseVersion?: number;
  source: string;
  targetRoles: string[];
}

export type PutResult = { document: DocRow } | { conflict: DocRow | null };
export type DeleteResult = { tombstone: DocRow } | { conflict: DocRow | null };
/** A feed entry: the current document, or a tombstone (deleted, or not visible to the caller). */
export type Change = { document: DocRow } | { tombstone: DocRow };

export const MAX_CONTENT_BYTES = 1 << 20;
export const MAX_FILES_BYTES = 4 << 20;
export const MAX_FILES = 200;
export const MAX_BATCH = 500;
const PAGE_BYTES = 4 << 20;
const COUNTER = "documents";
const PURGED_SETTING = "sync_tombstones_purged_through";

/** Kinds whose key is always "user". */
const SINGLETON_KINDS = new Set([DocumentKind.SETTINGS, DocumentKind.PERMISSION_RULES, DocumentKind.MEMORY, DocumentKind.HOOK]);
/** Keys allowed in SETTINGS.permissions; allow/ask/deny rules live in PERMISSION_RULES. */
const SETTINGS_PERMISSION_KEYS = new Set(["defaultMode", "additionalDirectories", "disableBypassPermissionsMode"]);
/** Kinds whose content must be a JSON object. */
const JSON_KINDS = new Set([DocumentKind.SETTINGS, DocumentKind.PERMISSION_RULES, DocumentKind.HOOK, DocumentKind.SSH_HOST, DocumentKind.POLICY]);

const writeMutex = new Mutex();

// --- version counter -------------------------------------------------------------------

/**
 * Takes the next global sync version inside `tx`; the only place that touches the counter.
 *
 * It is a row upsert, not a Postgres sequence, on purpose:
 * - the increment belongs to the writing transaction, so `readHead()` never returns a version whose row
 *   is not committed yet (with nextval() a reader could see head = N while N is still in flight, page
 *   past it and never deliver it); a rolled-back write also gives its number back;
 * - the upsert takes a row lock held until commit, so writers that reach this point commit in version
 *   order even across processes.
 * writeMutex stays: live events are published in-process right after commit, and publishing in version
 * order needs the whole "commit, then publish" step serialized, which a DB lock alone cannot give. It
 * also keeps waiting writers queued in memory instead of each holding a pooled connection on the lock.
 */
export async function nextSyncVersion(tx: Tx): Promise<number> {
  const [r] = await tx
    .insert(schema.syncCounter)
    .values({ name: COUNTER, value: 1 })
    .onConflictDoUpdate({ target: schema.syncCounter.name, set: { value: sql`${schema.syncCounter.value} + 1` } })
    .returning({ value: schema.syncCounter.value });
  return r!.value;
}

/** Highest committed sync version (0 when nothing was written yet). */
export async function readHead(): Promise<number> {
  await ensureDb();
  const r = await getDb().query.syncCounter.findFirst({ where: eq(schema.syncCounter.name, COUNTER) });
  return r?.value ?? 0;
}

// --- live events -------------------------------------------------------------------------

type Listener = (row: DocRow) => void;
const listeners = new Set<Listener>();
let lastPublished: number | undefined;

export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function publish(row: DocRow): void {
  lastPublished = row.version;
  for (const l of listeners) {
    try {
      l({ ...row });
    } catch (err) {
      console.error("[harness] sync listener failed", err);
    }
  }
}

/**
 * Highest version whose event has been published. Every change up to it has already been handed to
 * all current subscribers, so it is safe to report as a heartbeat cursor.
 */
export async function publishedHead(): Promise<number> {
  return writeMutex.run(async () => {
    lastPublished ??= await readHead();
    return lastPublished;
  });
}

export function currentPublishedHead(): number | undefined {
  return lastPublished;
}

// --- visibility --------------------------------------------------------------------------

function roles(row: DocRow): string[] {
  return Array.isArray(row.targetRoles) ? (row.targetRoles as string[]) : [];
}

function targets(row: DocRow, user: PublicUser): boolean {
  const r = roles(row);
  return r.length === 0 || r.includes(user.role);
}

/** Read access for GetDocument (admins can read every org document). */
function canRead(row: DocRow, user: PublicUser): boolean {
  if (row.scope === Scope.ORG) return user.role === "admin" || targets(row, user);
  return row.ownerId === user.id;
}

/** Maps a row to the caller's feed entry, or undefined if it is not part of the caller's feed. */
export function feedChange(row: DocRow, user: PublicUser, kinds: DocumentKind[] = []): Change | undefined {
  if (kinds.length && !kinds.includes(row.kind)) return undefined;
  if (row.scope === Scope.ORG) {
    return row.deletedAt || !targets(row, user) ? { tombstone: row } : { document: row };
  }
  if (row.ownerId !== user.id) return undefined;
  return row.deletedAt ? { tombstone: row } : { document: row };
}

// --- validation ------------------------------------------------------------------------

const PROJECT_KEY_RE = /^(path:[0-9a-f]{64}|[a-z0-9][a-z0-9.-]*(\/[^\s/]+)+)$/;

function validateRef(p: Principal, ref: DocRef | undefined): { ownerId: string } {
  if (!ref) throw new ApiError("invalid_request", "ref is required");
  if (![Scope.USER, Scope.ORG, Scope.PROJECT].includes(ref.scope)) throw new ApiError("invalid_request", "scope is required");
  if (!Object.values(DocumentKind).includes(ref.kind) || ref.kind === DocumentKind.UNSPECIFIED) {
    throw new ApiError("invalid_request", "kind is required");
  }
  if (ref.kind === DocumentKind.POLICY && ref.scope !== Scope.ORG) throw new ApiError("invalid_request", "POLICY documents exist only at org scope");
  if (ref.scope === Scope.PROJECT) {
    if (!ref.projectKey) throw new ApiError("invalid_request", "project_key is required for project scope");
    if (ref.projectKey.length > 300 || !PROJECT_KEY_RE.test(ref.projectKey)) {
      throw new ApiError("invalid_request", "project_key must be a normalized remote (host/owner/repo) or path:<sha256>");
    }
  } else if (ref.projectKey) {
    throw new ApiError("invalid_request", "project_key is only allowed with project scope");
  }
  if (!ref.key || ref.key.length > 200 || /[\u0000-\u001f\u007f]/.test(ref.key)) {
    throw new ApiError("invalid_request", "key must be 1-200 printable characters");
  }
  if (SINGLETON_KINDS.has(ref.kind) && ref.key !== "user") throw new ApiError("invalid_request", 'key must be "user" for this kind');
  if (ref.kind === DocumentKind.RULE && (!validRelativePath(ref.key) || !ref.key.endsWith(".md"))) {
    throw new ApiError("invalid_request", 'RULE keys are relative .md paths under the rules root, e.g. "frontend/react.md"');
  }
  if (ref.kind === DocumentKind.SETTINGS && ref.scope === Scope.PROJECT) {
    throw new ApiError("invalid_request", "Project-scope SETTINGS are not used (project settings live in the repository)");
  }
  if (ref.scope === Scope.ORG) {
    if (p.user.role !== "admin") throw new ApiError("permission", "Only admins can write org documents");
    return { ownerId: "" };
  }
  return { ownerId: p.user.id };
}

/** Kinds that may carry `files`: skill directories, memory imports / auto-memory topic files. */
const FILE_KINDS = new Set([DocumentKind.SKILL, DocumentKind.MEMORY]);

/** Relative "/"-separated path without empty, "." or ".." segments. */
function validRelativePath(path: string): boolean {
  if (!path || path.length > 300 || path.startsWith("/") || path.includes("\\") || path.includes("\0")) return false;
  return !path.split("/").some((seg) => seg === "" || seg === "." || seg === "..");
}

function validateFiles(kind: DocumentKind, files: FileInput[]): StoredFile[] {
  if (files.length === 0) return [];
  if (!FILE_KINDS.has(kind)) throw new ApiError("invalid_request", "files are only allowed for SKILL and MEMORY documents");
  if (files.length > MAX_FILES) throw new ApiError("invalid_request", `At most ${MAX_FILES} files per document`);
  const seen = new Set<string>();
  let total = 0;
  const out: StoredFile[] = [];
  for (const f of files) {
    const path = f.path;
    if (!validRelativePath(path)) {
      throw new ApiError("invalid_request", `Invalid file path ${JSON.stringify(path)}`);
    }
    if (seen.has(path)) throw new ApiError("invalid_request", `Duplicate file path ${path}`);
    seen.add(path);
    total += f.content.byteLength;
    out.push({ path, data: Buffer.from(f.content).toString("base64"), executable: !!f.executable });
  }
  if (total > MAX_FILES_BYTES) throw new ApiError("invalid_request", `Files exceed ${MAX_FILES_BYTES} bytes`);
  return out.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
}

function validateContent(kind: DocumentKind, content: string): void {
  if (Buffer.byteLength(content, "utf8") > MAX_CONTENT_BYTES) throw new ApiError("invalid_request", `content exceeds ${MAX_CONTENT_BYTES} bytes`);
  // Postgres text cannot store U+0000 (binary belongs in `files`).
  if (content.includes("\0")) throw new ApiError("invalid_request", "content must not contain NUL characters");
  if (JSON_KINDS.has(kind)) {
    let v: unknown;
    try {
      v = JSON.parse(content);
    } catch {
      throw new ApiError("invalid_request", "content must be valid JSON for this kind");
    }
    if (!v || typeof v !== "object" || Array.isArray(v)) throw new ApiError("invalid_request", "content must be a JSON object for this kind");
    if (kind === DocumentKind.SETTINGS) {
      const perms = (v as Record<string, unknown>).permissions;
      if (perms !== undefined) {
        if (!perms || typeof perms !== "object" || Array.isArray(perms)) throw new ApiError("invalid_request", "permissions must be an object");
        const extra = Object.keys(perms).filter((k) => !SETTINGS_PERMISSION_KEYS.has(k));
        if (extra.length) {
          throw new ApiError(
            "invalid_request",
            `settings.permissions may only contain ${[...SETTINGS_PERMISSION_KEYS].join(", ")}; rules belong in PERMISSION_RULES (got ${extra.join(", ")})`,
          );
        }
      }
    }
    if (kind === DocumentKind.PERMISSION_RULES) {
      for (const k of ["allow", "ask", "deny"]) {
        const list = (v as Record<string, unknown>)[k];
        if (list !== undefined && (!Array.isArray(list) || list.some((x) => typeof x !== "string"))) {
          throw new ApiError("invalid_request", `${k} must be an array of strings`);
        }
      }
    }
  }
}

function validateRoles(scope: Scope, roles: string[]): string[] {
  if (roles.length === 0) return [];
  if (scope !== Scope.ORG) throw new ApiError("invalid_request", "target_roles are only allowed at org scope");
  for (const r of roles) if (!r || r.length > 50) throw new ApiError("invalid_request", "Invalid target role");
  return [...new Set(roles)].sort();
}

/**
 * Lowercase hex SHA-256 over content and files in canonical order (files sorted by path):
 *   "harness-doc-v1\0" <len(content)> "\0" content, then per file "\0f\0" path "\0" (1|0) "\0" <len> "\0" bytes.
 */
export function contentHash(content: string, files: StoredFile[]): string {
  const h = createHash("sha256");
  const c = Buffer.from(content, "utf8");
  h.update(`harness-doc-v1\0${c.byteLength}\0`);
  h.update(c);
  for (const f of files) {
    const data = Buffer.from(f.data, "base64");
    h.update(`\0f\0${f.path}\0${f.executable ? 1 : 0}\0${data.byteLength}\0`);
    h.update(data);
  }
  return h.digest("hex");
}

function sameRoles(a: unknown, b: string[]): boolean {
  const x = Array.isArray(a) ? (a as string[]) : [];
  return x.length === b.length && x.every((r, i) => r === b[i]);
}

// --- tombstone retention -----------------------------------------------------------------

export function tombstoneRetentionMs(): number {
  const days = Number(process.env.HARNESS_SYNC_TOMBSTONE_DAYS ?? "");
  return (Number.isFinite(days) && days >= 30 ? days : 30) * 86_400_000;
}

export async function purgedThrough(): Promise<number> {
  await ensureDb();
  const r = await getDb().query.setting.findFirst({ where: eq(schema.setting.key, PURGED_SETTING) });
  return typeof r?.value === "number" ? r.value : 0;
}

/** Deletes tombstones older than the retention and advances the resync horizon. Returns the number purged. */
export async function purgeTombstones(now = Date.now(), retentionMs = tombstoneRetentionMs()): Promise<number> {
  await ensureDb();
  return writeMutex.run(async () => {
    const db = getDb();
    const cutoff = new Date(now - retentionMs);
    const old = and(isNotNull(schema.syncDocument.deletedAt), lt(schema.syncDocument.deletedAt, cutoff));
    const [top] = await db.select({ v: max(schema.syncDocument.version) }).from(schema.syncDocument).where(old);
    if (!top?.v) return 0;
    const prev = await purgedThrough();
    return db.transaction(async (tx) => {
      const gone = await tx.delete(schema.syncDocument).where(old).returning({ id: schema.syncDocument.id });
      if (gone.length) {
        await tx.delete(schema.syncDocumentRevision).where(inArray(schema.syncDocumentRevision.documentId, gone.map((g) => g.id)));
      }
      const value = Math.max(prev, top.v!);
      await tx
        .insert(schema.setting)
        .values({ key: PURGED_SETTING, value, updatedAt: new Date(now) })
        .onConflictDoUpdate({ target: schema.setting.key, set: { value, updatedAt: new Date(now) } });
      return gone.length;
    });
  });
}

let lastPurgeAt = 0;
function maybePurge(): void {
  const now = Date.now();
  if (now - lastPurgeAt < 3_600_000) return;
  lastPurgeAt = now;
  purgeTombstones(now).catch((err) => console.error("[harness] tombstone purge failed", err));
}

// --- reads -----------------------------------------------------------------------------

export interface ChangesPage {
  changes: Change[];
  nextVersion: number;
  hasMore: boolean;
  resyncRequired: boolean;
  /** Continuation of this scan when hasMore (opaque to clients). */
  nextPageToken: string;
}

function encodePageToken(version: number, userId: string): string {
  return Buffer.from(JSON.stringify({ v: version, u: userId })).toString("base64url");
}

function decodePageToken(token: string, userId: string): number {
  try {
    const t = JSON.parse(Buffer.from(token, "base64url").toString("utf8"));
    if (typeof t.v === "number" && Number.isInteger(t.v) && t.v >= 0 && t.u === userId) return t.v;
  } catch {
    // fall through
  }
  throw new ApiError("invalid_request", "Invalid page_token");
}

/**
 * One page of the caller's feed after `sinceVersion`, or after the position in `pageToken` (a
 * continuation, which never answers resync_required so a long scan from 0 can always finish).
 */
export async function listChanges(
  p: Principal,
  sinceVersion: number,
  kinds: DocumentKind[],
  pageSize?: number,
  pageToken?: string,
): Promise<ChangesPage> {
  await ensureDb();
  const since = pageToken ? decodePageToken(pageToken, p.user.id) : Math.max(0, Math.floor(sinceVersion));
  const size = Math.min(Math.max(pageSize || 200, 1), 1000);
  const head = await readHead();
  // A cursor older than purged tombstones, or ahead of the server (database restored), needs a full resync.
  if (!pageToken && since > 0 && (since < (await purgedThrough()) || since > head)) {
    return { changes: [], nextVersion: since, hasMore: false, resyncRequired: true, nextPageToken: "" };
  }
  const d = schema.syncDocument;
  const rows = await getDb()
    .select()
    .from(d)
    .where(
      and(
        gt(d.version, since),
        lte(d.version, head),
        kinds.length ? inArray(d.kind, kinds) : undefined,
        or(and(inArray(d.scope, [Scope.USER, Scope.PROJECT]), eq(d.ownerId, p.user.id)), eq(d.scope, Scope.ORG)),
      ),
    )
    .orderBy(asc(d.version))
    .limit(size + 1);

  const changes: Change[] = [];
  let bytes = 0;
  let cut = rows.length > size;
  for (const row of rows.slice(0, size)) {
    const ch = feedChange(row, p.user, kinds);
    if (!ch) continue;
    const sz = row.content.length + JSON.stringify(row.files).length;
    if (changes.length > 0 && bytes + sz > PAGE_BYTES) {
      cut = true;
      break;
    }
    bytes += sz;
    changes.push(ch);
  }
  const last = changes.length ? ("document" in changes[changes.length - 1]! ? (changes.at(-1) as { document: DocRow }).document : (changes.at(-1) as { tombstone: DocRow }).tombstone) : undefined;
  const nextVersion = cut ? last!.version : Math.max(since, head);
  return {
    changes,
    nextVersion,
    hasMore: cut,
    resyncRequired: false,
    nextPageToken: cut ? encodePageToken(nextVersion, p.user.id) : "",
  };
}

export async function getDocument(p: Principal, sel: { id: string } | { ref: DocRef }): Promise<DocRow> {
  await ensureDb();
  const d = schema.syncDocument;
  let row: DocRow | undefined;
  if ("id" in sel) {
    if (!sel.id) throw new ApiError("invalid_request", "id or ref is required");
    row = await getDb().query.syncDocument.findFirst({ where: eq(d.id, sel.id) });
  } else {
    const ref = sel.ref;
    if (!ref || !ref.key) throw new ApiError("invalid_request", "ref.key is required");
    const ownerId = ref.scope === Scope.ORG ? "" : p.user.id;
    row = await getDb().query.syncDocument.findFirst({
      where: and(eq(d.ownerId, ownerId), eq(d.scope, ref.scope), eq(d.projectKey, ref.projectKey ?? ""), eq(d.kind, ref.kind), eq(d.key, ref.key)),
    });
  }
  if (!row || row.deletedAt || !canRead(row, p.user)) throw new ApiError("not_found", "Document not found");
  return row;
}

// --- writes ----------------------------------------------------------------------------

export async function putDocument(p: Principal, input: PutInput): Promise<PutResult> {
  const { ownerId } = validateRef(p, input.ref);
  const ref = input.ref;
  if (ref.kind === DocumentKind.MCP_SERVER) throw new ApiError("invalid_request", "MCP_SERVER entries are managed by McpService");
  validateContent(ref.kind, input.content);
  const files = validateFiles(ref.kind, input.files);
  const targetRoles = validateRoles(ref.scope, input.targetRoles);
  if (input.baseVersion !== undefined && (!Number.isInteger(input.baseVersion) || input.baseVersion < 0)) {
    throw new ApiError("invalid_request", "base_version must be >= 0");
  }
  const source = (input.source || (p.deviceId ? `client:${p.deviceId}` : "")).slice(0, 200);
  const hash = contentHash(input.content, files);
  await ensureDb();
  maybePurge();

  const result = await writeMutex.run(async () => {
    lastPublished ??= await readHead();
    const d = schema.syncDocument;
    return getDb().transaction(async (tx): Promise<PutResult & { changed?: boolean }> => {
      const [cur] = await tx
        .select()
        .from(d)
        .where(and(eq(d.ownerId, ownerId), eq(d.scope, ref.scope), eq(d.projectKey, ref.projectKey), eq(d.kind, ref.kind), eq(d.key, ref.key)))
        .for("update");
      const live = cur && !cur.deletedAt ? cur : undefined;
      if (input.baseVersion === 0 && live) return { conflict: live };
      if (input.baseVersion !== undefined && input.baseVersion > 0 && (!live || live.version !== input.baseVersion)) {
        return { conflict: cur ?? null };
      }
      // Writing identical content is a no-op: no new version, no feed churn.
      if (live && live.contentHash === hash && sameRoles(live.targetRoles, targetRoles)) return { document: live };

      const version = await nextSyncVersion(tx);
      const now = new Date();
      const values = {
        version,
        content: input.content,
        files,
        contentHash: hash,
        source,
        targetRoles,
        updatedAt: now,
        updatedBy: p.user.id,
        deletedAt: null,
      };
      let row: DocRow;
      if (cur) {
        [row] = await tx
          .update(d)
          .set({ ...values, createdAt: cur.deletedAt ? now : cur.createdAt })
          .where(eq(d.id, cur.id))
          .returning() as [DocRow];
      } else {
        [row] = await tx
          .insert(d)
          .values({ id: newId("doc"), ownerId, scope: ref.scope, projectKey: ref.projectKey, kind: ref.kind, key: ref.key, createdAt: now, ...values })
          .returning() as [DocRow];
      }
      await tx.insert(schema.syncDocumentRevision).values({
        documentId: row.id,
        version,
        content: input.content,
        files,
        contentHash: hash,
        deleted: false,
        updatedBy: p.user.id,
        createdAt: now,
      });
      return { document: row, changed: true };
    }).then((r) => {
      if (r.changed && "document" in r) publish(r.document);
      return r;
    });
  });
  if ("document" in result) return { document: result.document };
  return { conflict: result.conflict };
}

export async function deleteDocument(p: Principal, id: string, baseVersion?: number): Promise<DeleteResult> {
  if (!id) throw new ApiError("invalid_request", "id is required");
  if (baseVersion !== undefined && (!Number.isInteger(baseVersion) || baseVersion <= 0)) {
    throw new ApiError("invalid_request", "base_version must be a positive version");
  }
  await ensureDb();
  maybePurge();
  return writeMutex.run(async () => {
    lastPublished ??= await readHead();
    const d = schema.syncDocument;
    let published: DocRow | undefined;
    const r = await getDb().transaction(async (tx): Promise<DeleteResult> => {
      const [cur] = await tx.select().from(d).where(eq(d.id, id)).for("update");
      if (!cur || !canRead(cur, p.user)) throw new ApiError("not_found", "Document not found");
      if (cur.scope === Scope.ORG && p.user.role !== "admin") throw new ApiError("permission", "Only admins can delete org documents");
      if (cur.kind === DocumentKind.MCP_SERVER) throw new ApiError("invalid_request", "MCP_SERVER entries are managed by McpService");
      if (cur.deletedAt) return baseVersion === undefined ? { tombstone: cur } : { conflict: cur };
      if (baseVersion !== undefined && cur.version !== baseVersion) return { conflict: cur };
      const version = await nextSyncVersion(tx);
      const now = new Date();
      const [row] = (await tx
        .update(d)
        .set({ version, content: "", files: [], contentHash: contentHash("", []), updatedAt: now, updatedBy: p.user.id, deletedAt: now })
        .where(eq(d.id, id))
        .returning()) as [DocRow];
      await tx.insert(schema.syncDocumentRevision).values({
        documentId: id,
        version,
        content: "",
        files: [],
        contentHash: row.contentHash,
        deleted: true,
        updatedBy: p.user.id,
        createdAt: now,
      });
      published = row;
      return { tombstone: row };
    });
    if (published) publish(published);
    return r;
  });
}

/** Files of a stored row, decoded. */
export function rowFiles(row: DocRow): FileInput[] {
  const files = Array.isArray(row.files) ? (row.files as StoredFile[]) : [];
  return files.map((f) => ({ path: f.path, content: new Uint8Array(Buffer.from(f.data, "base64")), executable: f.executable }));
}
