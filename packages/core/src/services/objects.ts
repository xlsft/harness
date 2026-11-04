/**
 * The object store: what users upload (attachments, images in messages, avatars) and the files the model made for
 * itself (scratch files, saved tool results, screenshots, SendUserFile). Transport-agnostic; src/rpc/objects.ts and
 * src/http/routes/object.ts adapt it. Bytes go through src/services/blobstore.ts and clients never reach that directly.
 *
 * Content-addressed per owner: the bytes of one (owner, sha256) live once under u/<owner>/<sha256>, and `stored_object`
 * has a row per use of them (session, kind, name). Uploading known bytes adds a row and sends nothing. The bytes go when
 * the last row does; if that delete fails or a user was deleted, the sweep finds them later.
 *
 * Uploads map one to one onto S3 multipart: a chunk is a part, `object_upload` remembers the part etags, so an upload
 * resumes after a restart of either side. At the end the whole file is read back and checked against its sha256, and
 * only then a row exists: until then the bytes under the key are unreachable.
 */
import { createHash } from "node:crypto";
import { and, asc, count, eq, inArray, like, lt, ne, notExists, sql } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { blobStore, type BlobRange } from "./blobstore.ts";
import type { Principal } from "./devices.ts";
import { objectQuotaFor } from "./quotas.ts";

export type ObjectKindName = "attachment" | "avatar" | "scratch" | "tool_result" | "image" | "sent_file";

export interface ObjectUse {
  session_id: string;
  kind: ObjectKindName;
  name: string;
  content_type: string;
}

export type ObjectRow = typeof schema.storedObject.$inferSelect;

/** One chunk is one S3 part, and S3 takes no part under 5 MiB but the last. */
export const OBJECT_CHUNK_BYTES = 8 << 20;
const DEFAULT_MAX_FILE_BYTES = 1 << 30;
const DEFAULT_QUOTA_BYTES = 10 * (1 << 30);
const NAME_MAX = 255;
const SHA256_RE = /^[0-9a-f]{64}$/;
const EMPTY_SHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

function envBytes(name: string, def: number): number {
  const v = process.env[name];
  if (v === undefined || v === "") return def;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) ? n : def;
}

/** HARNESS_OBJECTS_MAX_FILE_BYTES: the largest file core takes (default 1 GiB). */
export function maxFileBytes(): number {
  return envBytes("HARNESS_OBJECTS_MAX_FILE_BYTES", DEFAULT_MAX_FILE_BYTES);
}

/** HARNESS_OBJECTS_QUOTA_BYTES: everyone's quota unless /api/admin/quotas says otherwise (default 10 GiB, negative: unlimited). */
export function defaultQuotaBytes(): number {
  return envBytes("HARNESS_OBJECTS_QUOTA_BYTES", DEFAULT_QUOTA_BYTES);
}

function blobKey(owner: string, sha256: string): string {
  return `u/${owner}/${sha256}`;
}

function checkSha256(sha256: string): void {
  if (!SHA256_RE.test(sha256)) throw new ApiError("invalid_request", "sha256 must be 64 lowercase hex characters");
}

function checkUse(use: ObjectUse): void {
  if (use.session_id.length > 200) throw new ApiError("invalid_request", "session_id is too long");
  if (use.name.length > NAME_MAX) throw new ApiError("invalid_request", `Names have at most ${NAME_MAX} characters`);
  if (use.content_type.length > 200) throw new ApiError("invalid_request", "content_type is too long");
}

/** Bytes the owner's objects take, each stored object once. */
export async function usedBytes(owner: string): Promise<number> {
  const o = schema.storedObject;
  const inner = getDb().select({ size: sql<string>`max(${o.size})`.as("size") }).from(o).where(eq(o.ownerId, owner)).groupBy(o.sha256).as("blobs");
  const [row] = await getDb().select({ total: sql<string>`coalesce(sum(${inner.size}), 0)` }).from(inner);
  return Number(row?.total ?? 0);
}

async function firstRow(owner: string, sha256: string): Promise<ObjectRow | undefined> {
  const o = schema.storedObject;
  return getDb().query.storedObject.findFirst({ where: and(eq(o.ownerId, owner), eq(o.sha256, sha256)), orderBy: asc(o.createdAt) });
}

/** Adds the record of one use; the same use again only touches it. */
async function addRecord(owner: string, sha256: string, size: number, use: ObjectUse): Promise<ObjectRow> {
  const o = schema.storedObject;
  const now = new Date();
  const [row] = await getDb()
    .insert(o)
    .values({
      id: newId("obj"),
      ownerId: owner,
      sessionId: use.session_id,
      kind: use.kind,
      name: use.name,
      sha256,
      size,
      contentType: use.content_type || "application/octet-stream",
      createdAt: now,
      updatedAt: now,
    })
    .onConflictDoUpdate({ target: [o.ownerId, o.sessionId, o.kind, o.sha256, o.name], set: { updatedAt: now, contentType: use.content_type || "application/octet-stream" } })
    .returning();
  return row!;
}

// --- upload -----------------------------------------------------------------------------------------------

export type StartUploadResult =
  | { already_stored: true; record: ObjectRow; chunk_size: number }
  | { already_stored: false; upload_id: string; chunk_size: number; received_chunks: number[] };

export async function startUpload(p: Principal, input: { sha256: string; size: number; use: ObjectUse }): Promise<StartUploadResult> {
  await ensureDb();
  const owner = p.user.id;
  checkSha256(input.sha256);
  checkUse(input.use);
  if (!Number.isSafeInteger(input.size) || input.size < 0) throw new ApiError("invalid_request", "size must not be negative");
  if (input.size > maxFileBytes()) throw new ApiError("invalid_request", `The file is larger than the server's limit of ${maxFileBytes()} bytes`, { extra: { reason: "too_large" } });

  const known = await firstRow(owner, input.sha256);
  if (known) {
    if (known.size !== input.size) throw new ApiError("invalid_request", "size does not match the stored object");
    return { already_stored: true, record: await addRecord(owner, input.sha256, known.size, input.use), chunk_size: OBJECT_CHUNK_BYTES };
  }
  await checkQuota(p, input.size);
  if (input.size === 0) {
    if (input.sha256 !== EMPTY_SHA256) throw new ApiError("invalid_request", "sha256 does not match an empty file");
    await blobStore().put(blobKey(owner, input.sha256), new Uint8Array());
    return { already_stored: true, record: await addRecord(owner, input.sha256, 0, input.use), chunk_size: OBJECT_CHUNK_BYTES };
  }

  const u = schema.objectUpload;
  const db = getDb();
  let row = await db.query.objectUpload.findFirst({ where: and(eq(u.ownerId, owner), eq(u.sha256, input.sha256)) });
  if (!row) {
    const key = blobKey(owner, input.sha256);
    const storeUploadId = await blobStore().startMultipart(key);
    const now = new Date();
    const [created] = await db
      .insert(u)
      .values({ id: newId("upl"), ownerId: owner, sha256: input.sha256, size: input.size, storeUploadId, parts: {}, createdAt: now, updatedAt: now })
      .onConflictDoNothing({ target: [u.ownerId, u.sha256] })
      .returning();
    if (created) row = created;
    else {
      // Another device started the same file a moment ago: join that upload.
      await blobStore().abortMultipart(key, storeUploadId);
      row = await db.query.objectUpload.findFirst({ where: and(eq(u.ownerId, owner), eq(u.sha256, input.sha256)) });
      if (!row) throw new ApiError("conflict", "The upload was finished or abandoned concurrently; start again");
    }
  }
  if (row.size !== input.size) throw new ApiError("invalid_request", "size does not match the upload in progress");
  return {
    already_stored: false,
    upload_id: row.id,
    chunk_size: OBJECT_CHUNK_BYTES,
    received_chunks: Object.keys(row.parts as Record<string, string>).map(Number).sort((a, b) => a - b),
  };
}

async function checkQuota(p: Principal, adding: number): Promise<void> {
  const quota = await objectQuotaFor(p.user, defaultQuotaBytes());
  if (quota < 0) return;
  const used = await usedBytes(p.user.id);
  if (used + adding > quota) throw new ApiError("quota_exceeded", `Object storage quota exceeded (${used} of ${quota} bytes used)`);
}

async function ownUpload(p: Principal, uploadId: string) {
  const u = schema.objectUpload;
  const row = await getDb().query.objectUpload.findFirst({ where: and(eq(u.id, uploadId), eq(u.ownerId, p.user.id)) });
  if (!row) throw new ApiError("not_found", `Upload ${uploadId} not found`);
  return row;
}

function chunkCount(size: number): number {
  return Math.ceil(size / OBJECT_CHUNK_BYTES);
}

export async function uploadChunk(p: Principal, input: { upload_id: string; index: number; data: Uint8Array }): Promise<void> {
  await ensureDb();
  const row = await ownUpload(p, input.upload_id);
  const chunks = chunkCount(row.size);
  if (!Number.isInteger(input.index) || input.index < 0 || input.index >= chunks) throw new ApiError("invalid_request", `index must be between 0 and ${chunks - 1}`);
  const want = input.index === chunks - 1 ? row.size - OBJECT_CHUNK_BYTES * (chunks - 1) : OBJECT_CHUNK_BYTES;
  if (input.data.length !== want) throw new ApiError("invalid_request", `Chunk ${input.index} must be ${want} bytes (got ${input.data.length})`);
  const etag = await blobStore().putPart(blobKey(row.ownerId, row.sha256), row.storeUploadId, input.index + 1, input.data);
  const u = schema.objectUpload;
  await getDb()
    .update(u)
    .set({ parts: sql`${u.parts} || ${JSON.stringify({ [input.index]: etag })}::jsonb`, updatedAt: new Date() })
    .where(eq(u.id, row.id));
}

async function matches(key: string, sha256: string, size: number): Promise<boolean> {
  const body = await blobStore().get(key);
  if (!body) return false;
  const hash = createHash("sha256");
  let n = 0;
  const reader = body.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.length;
    hash.update(value);
  }
  return n === size && hash.digest("hex") === sha256;
}

export async function finishUpload(p: Principal, input: { upload_id: string; use: ObjectUse }): Promise<ObjectRow> {
  await ensureDb();
  checkUse(input.use);
  const row = await ownUpload(p, input.upload_id);
  const parts = row.parts as Record<string, string>;
  const chunks = chunkCount(row.size);
  const missing: number[] = [];
  for (let i = 0; i < chunks; i++) if (!parts[i]) missing.push(i);
  if (missing.length) throw new ApiError("failed_precondition", `Chunks not received yet: ${missing.slice(0, 20).join(", ")}`);

  const store = blobStore();
  const key = blobKey(row.ownerId, row.sha256);
  try {
    await store.completeMultipart(key, row.storeUploadId, Object.entries(parts).map(([i, etag]) => ({ partNumber: Number(i) + 1, etag })));
  } catch (err) {
    // A finish that died after the store completed the upload: the store no longer knows it, but the bytes are there.
    if (!(await matches(key, row.sha256, row.size).catch(() => false))) throw err;
  }
  const u = schema.objectUpload;
  if (!(await matches(key, row.sha256, row.size))) {
    await store.delete(key);
    await getDb().delete(u).where(eq(u.id, row.id));
    throw new ApiError("invalid_request", "The uploaded bytes do not match sha256");
  }
  const record = await addRecord(row.ownerId, row.sha256, row.size, input.use);
  await getDb().delete(u).where(eq(u.id, row.id));
  return record;
}

/** Stores small bytes held in memory (avatars). */
export async function putObject(owner: string, data: Uint8Array, use: ObjectUse): Promise<ObjectRow> {
  await ensureDb();
  const sha256 = createHash("sha256").update(data).digest("hex");
  if (!(await firstRow(owner, sha256))) await blobStore().put(blobKey(owner, sha256), data);
  return addRecord(owner, sha256, data.length, use);
}

/** The bytes of an object held in memory (avatars); undefined when it is not there. */
export async function readObjectBytes(owner: string, sha256: string): Promise<Uint8Array | undefined> {
  const body = await blobStore().get(blobKey(owner, sha256));
  if (!body) return undefined;
  return new Uint8Array(await new Response(body).arrayBuffer());
}

// --- metadata ---------------------------------------------------------------------------------------------

/** A record of the owner's object, if any. */
export async function statObjectOf(owner: string, sha256: string): Promise<ObjectRow | undefined> {
  return firstRow(owner, sha256);
}

export async function statObject(p: Principal, sha256: string): Promise<ObjectRow> {
  await ensureDb();
  checkSha256(sha256);
  const row = await firstRow(p.user.id, sha256);
  if (!row) throw new ApiError("not_found", `Object ${sha256} not found`);
  return row;
}

export async function listSessionObjects(p: Principal, input: { session_id: string; page_size?: number; page_token?: string }): Promise<{ records: ObjectRow[]; next_page_token: string }> {
  await ensureDb();
  if (!input.session_id) throw new ApiError("invalid_request", "session_id is required");
  const limit = Math.min(input.page_size || 100, 500);
  const offset = input.page_token ? Number.parseInt(input.page_token, 10) || 0 : 0;
  const o = schema.storedObject;
  const records = await getDb().query.storedObject.findMany({
    where: and(eq(o.ownerId, p.user.id), eq(o.sessionId, input.session_id)),
    orderBy: [asc(o.createdAt), asc(o.id)],
    limit: limit + 1,
    offset,
  });
  const more = records.length > limit;
  return { records: records.slice(0, limit), next_page_token: more ? String(offset + limit) : "" };
}

export async function objectLimits(p: Principal): Promise<{ max_file_bytes: number; quota_bytes: number; used_bytes: number; chunk_size: number }> {
  await ensureDb();
  return {
    max_file_bytes: maxFileBytes(),
    quota_bytes: await objectQuotaFor(p.user, defaultQuotaBytes()),
    used_bytes: await usedBytes(p.user.id),
    chunk_size: OBJECT_CHUNK_BYTES,
  };
}

// --- delete -----------------------------------------------------------------------------------------------

/** Deletes the bytes of the given objects that no record uses anymore. Best effort: the sweep repeats what fails here. */
async function dropUnreferenced(owner: string, sha256s: Iterable<string>): Promise<void> {
  const o = schema.storedObject;
  for (const sha256 of new Set(sha256s)) {
    const [left] = await getDb().select({ n: count() }).from(o).where(and(eq(o.ownerId, owner), eq(o.sha256, sha256)));
    if (left && left.n > 0) continue;
    await blobStore()
      .delete(blobKey(owner, sha256))
      .catch((err) => console.error("[harness] object store: delete failed; the sweep will retry", err));
  }
}

export async function deleteObject(p: Principal, input: { sha256: string; session_id?: string; kind?: ObjectKindName; name?: string }): Promise<void> {
  await ensureDb();
  checkSha256(input.sha256);
  const o = schema.storedObject;
  const where = [eq(o.ownerId, p.user.id), eq(o.sha256, input.sha256)];
  if (input.session_id) {
    where.push(eq(o.sessionId, input.session_id));
    if (input.kind) where.push(eq(o.kind, input.kind));
    if (input.name) where.push(eq(o.name, input.name));
  }
  const rows = await getDb().delete(o).where(and(...where)).returning({ sha256: o.sha256 });
  if (!rows.length) throw new ApiError("not_found", `Object ${input.sha256} not found`);
  await dropUnreferenced(p.user.id, [input.sha256]);
}

/** Deletes the records of a kind for an owner except the given object (the previous avatar). */
export async function deleteOtherObjects(owner: string, kind: ObjectKindName, keepSha256: string): Promise<void> {
  const o = schema.storedObject;
  const where = [eq(o.ownerId, owner), eq(o.kind, kind), eq(o.sessionId, "")];
  if (keepSha256) where.push(ne(o.sha256, keepSha256));
  const rows = await getDb().delete(o).where(and(...where)).returning({ sha256: o.sha256 });
  await dropUnreferenced(owner, rows.map((r) => r.sha256));
}

/** A session was deleted: its records go, and the bytes no other session or avatar still uses. */
export async function deleteSessionObjects(owner: string, sessionId: string): Promise<void> {
  const o = schema.storedObject;
  const rows = await getDb().delete(o).where(and(eq(o.ownerId, owner), eq(o.sessionId, sessionId))).returning({ sha256: o.sha256 });
  await dropUnreferenced(owner, rows.map((r) => r.sha256));
}

// --- serving ----------------------------------------------------------------------------------------------

/** Rendered when opened: raster images and PDFs. Nothing here can run script on our origin. */
const INLINE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "application/pdf"]);

/** Keep their type, for <video>, <audio> and readers, but download when opened. Inert in a browser. */
const ATTACHMENT_TYPES = new Set([
  "video/mp4",
  "video/webm",
  "video/ogg",
  "video/quicktime",
  "audio/mpeg",
  "audio/mp4",
  "audio/ogg",
  "audio/wav",
  "audio/webm",
  "audio/flac",
  "text/plain",
  "text/csv",
  "text/markdown",
  "application/json",
  "application/zip",
  "application/gzip",
  "application/x-tar",
]);

function bareType(contentType: string): string {
  return (contentType.split(";")[0] ?? "").trim().toLowerCase();
}

function contentDisposition(mode: "inline" | "attachment", name: string): string {
  const clean = name.replace(/[\r\n"\\/]/g, "_").trim();
  if (!clean) return mode;
  const ascii = clean.replace(/[^\x20-\x7e]/g, "_");
  return `${mode}; filename="${ascii}"; filename*=UTF-8''${encodeURIComponent(clean).replace(/['()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`)}`;
}

/**
 * The response headers of user content. Whatever the uploader claimed: only raster images and PDFs render, a type
 * outside the two lists (HTML, SVG, scripts, XML...) goes out as opaque bytes, nothing is sniffed, and if a browser
 * renders it anyway the sandbox gives it an opaque origin without scripts.
 */
export function servedHeaders(row: Pick<ObjectRow, "contentType" | "name" | "sha256">): Record<string, string> {
  const type = bareType(row.contentType);
  const inline = INLINE_TYPES.has(type);
  return {
    "content-type": inline || ATTACHMENT_TYPES.has(type) ? type : "application/octet-stream",
    "content-disposition": contentDisposition(inline ? "inline" : "attachment", row.name),
    "x-content-type-options": "nosniff",
    "content-security-policy": "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'",
    "referrer-policy": "no-referrer",
    // Content-addressed: the bytes of this URL never change. Private: only its owner may have them.
    "cache-control": "private, max-age=31536000, immutable",
    etag: `"${row.sha256}"`,
    "accept-ranges": "bytes",
  };
}

/** Parses one Range header against a size. undefined: ignore it and send everything; "invalid": answer 416. */
export function parseRange(header: string | null, size: number): BlobRange | undefined | "invalid" {
  if (!header) return undefined;
  const m = /^bytes=(\d*)-(\d*)$/.exec(header.trim());
  // Multiple ranges and other units may be ignored (RFC 9110).
  if (!m || (!m[1] && !m[2])) return undefined;
  if (!m[1]) {
    const n = Number(m[2]);
    if (n === 0 || size === 0) return "invalid";
    return { start: Math.max(size - n, 0), end: size - 1 };
  }
  const start = Number(m[1]);
  if (start >= size) return "invalid";
  const end = m[2] ? Math.min(Number(m[2]), size - 1) : size - 1;
  if (end < start) return undefined;
  return { start, end };
}

/** Answers GET and HEAD /api/objects/<sha256>: only the owner's objects exist for a caller. */
export async function serveObject(p: Principal, sha256: string, method: string, headers: Headers): Promise<Response> {
  const row = await statObject(p, sha256);
  const out = servedHeaders(row);
  if (headers.get("if-none-match") === out.etag) return new Response(null, { status: 304, headers: out });
  const range = parseRange(headers.get("range"), row.size);
  if (range === "invalid") return new Response(null, { status: 416, headers: { ...out, "content-range": `bytes */${row.size}` } });
  if (range) out["content-range"] = `bytes ${range.start}-${range.end}/${row.size}`;
  out["content-length"] = String(range ? range.end - range.start + 1 : row.size);
  const status = range ? 206 : 200;
  if (method === "HEAD" || row.size === 0) return new Response(null, { status, headers: out });
  const body = await blobStore().get(blobKey(row.ownerId, row.sha256), range);
  if (!body) throw new ApiError("not_found", `Object ${sha256} not found`);
  return new Response(body, { status, headers: out });
}

// --- sweep ------------------------------------------------------------------------------------------------

/** How long bytes without a record may stay: a finish in progress, a session that has yet to arrive. */
const BLOB_GRACE_MS = 24 * 3600_000;
const UPLOAD_TTL_MS = 7 * 24 * 3600_000;
const SESSION_GRACE_MS = 7 * 24 * 3600_000;

export interface SweepReport {
  records: number;
  uploads: number;
  blobs: number;
}

/**
 * Removes what nothing refers to: records of sessions that are gone (a client uploaded after the delete, or the session
 * never arrived), uploads abandoned for a week, and bytes without a record (a failed delete, a deleted user).
 */
export async function sweepObjects(now: Date = new Date()): Promise<SweepReport> {
  await ensureDb();
  const db = getDb();
  const o = schema.storedObject;
  const s = schema.syncSession;
  const u = schema.objectUpload;
  const report: SweepReport = { records: 0, uploads: 0, blobs: 0 };

  const gone = await db
    .delete(o)
    .where(
      and(
        ne(o.sessionId, ""),
        lt(o.createdAt, new Date(now.getTime() - SESSION_GRACE_MS)),
        notExists(db.select({ id: s.id }).from(s).where(and(eq(s.id, o.sessionId), eq(s.userId, o.ownerId)))),
      ),
    )
    .returning({ ownerId: o.ownerId, sha256: o.sha256 });
  report.records = gone.length;

  const stale = await db.select().from(u).where(lt(u.updatedAt, new Date(now.getTime() - UPLOAD_TTL_MS)));
  for (const row of stale) {
    await blobStore()
      .abortMultipart(blobKey(row.ownerId, row.sha256), row.storeUploadId)
      .catch((err) => console.error("[harness] object store: abort failed", err));
    await db.delete(u).where(eq(u.id, row.id));
    report.uploads++;
  }

  // Bytes without a record, a page at a time. Anything younger than the grace may belong to a finish in progress.
  const cutoff = now.getTime() - BLOB_GRACE_MS;
  let after: string | undefined;
  do {
    const page = await blobStore().list("u/", after);
    after = page.next;
    const old = page.items.filter((i) => i.lastModified.getTime() < cutoff);
    const known = new Set<string>();
    if (old.length) {
      const rows = await db
        .selectDistinct({ ownerId: o.ownerId, sha256: o.sha256 })
        .from(o)
        .where(inArray(o.sha256, old.map((i) => i.key.split("/").pop()!)));
      for (const r of rows) known.add(blobKey(r.ownerId, r.sha256));
    }
    for (const item of old) {
      if (known.has(item.key)) continue;
      await blobStore().delete(item.key);
      report.blobs++;
    }
  } while (after);
  return report;
}

let sweepTimer: ReturnType<typeof setInterval> | undefined;

/** Runs the sweep at startup and then every HARNESS_OBJECTS_SWEEP_MS (default 6 hours, 0: never), with `extra` before it. */
export function startObjectSweep(extra?: () => Promise<void>): void {
  const interval = envBytes("HARNESS_OBJECTS_SWEEP_MS", 6 * 3600_000);
  if (interval <= 0 || sweepTimer) return;
  const run = () => {
    void (async () => {
      await extra?.();
      await sweepObjects();
    })().catch((err) => console.error("[harness] object sweep failed", err));
  };
  run();
  sweepTimer = setInterval(run, interval);
  sweepTimer.unref();
}

/** The users whose image column still holds a data URL (avatars from before the store). */
export async function usersWithInlineAvatars(): Promise<{ id: string; image: string }[]> {
  await ensureDb();
  const us = schema.user;
  const rows = await getDb().select({ id: us.id, image: us.image }).from(us).where(like(us.image, "data:%"));
  return rows.map((r) => ({ id: r.id, image: r.image! }));
}
