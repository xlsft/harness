/**
 * Artifacts: pages and files published from sessions (the ArtifactPublish tool), listed on the Artifacts screen. Private
 * to the owner until shared with the server's other users. The bytes are object store rows of kind "artifact" with no
 * session, so they outlive the session that made them and count against the owner's quota.
 *
 * What protects the app from a page is on the bytes themselves: every answer carries a CSP `sandbox` without
 * allow-same-origin, so a page runs in an opaque origin however it is reached, a frame or a pasted link, and it has no
 * network to send anything anywhere.
 */
import { and, desc, eq, ilike, inArray, ne, or, sql } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import type { Principal } from "./devices.ts";
import { addObjectRecord, deleteObjectRecords, openObject, readObjectBytes, statObjectOf } from "./objects.ts";

export type ArtifactVisibility = "private" | "shared";
export type ArtifactKindName = "page" | "markdown" | "text" | "image" | "pdf" | "file";

type ArtifactRow = typeof schema.artifact.$inferSelect;
type VersionRow = typeof schema.artifactVersion.$inferSelect;

export interface ArtifactView {
  row: ArtifactRow;
  version: VersionRow;
  kind: ArtifactKindName;
  mine: boolean;
  ownerName: string;
}

const TITLE_MAX = 200;
const NAME_MAX = 255;
/** The most of a text file GetArtifact returns inline. */
const TEXT_INLINE_MAX = 512 << 10;

/** Types shown as what they are in the viewer; the rest is a download. */
const IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "image/svg+xml"]);
const TEXT_EXTENSIONS = new Set(["txt", "log", "csv", "tsv", "json", "yaml", "yml", "toml", "xml", "js", "ts", "py", "go", "rs", "sh", "css", "sql"]);
const TYPE_BY_EXTENSION: Record<string, string> = {
  html: "text/html",
  htm: "text/html",
  md: "text/markdown",
  markdown: "text/markdown",
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
  pdf: "application/pdf",
};

function bareType(contentType: string): string {
  return contentType.split(";")[0]!.trim().toLowerCase();
}

/** The file's type: its extension decides when it has a known one, since that is what the publisher chose to call it. */
export function artifactType(name: string, contentType: string): string {
  const ext = name.includes(".") ? name.split(".").pop()!.toLowerCase() : "";
  return TYPE_BY_EXTENSION[ext] ?? (TEXT_EXTENSIONS.has(ext) ? "text/plain" : bareType(contentType) || "application/octet-stream");
}

export function artifactKind(type: string): ArtifactKindName {
  const t = bareType(type);
  if (t === "text/html" || t === "application/xhtml+xml") return "page";
  if (t === "text/markdown") return "markdown";
  if (IMAGE_TYPES.has(t)) return "image";
  if (t === "application/pdf") return "pdf";
  if (t.startsWith("text/") || t === "application/json") return "text";
  return "file";
}

/**
 * The policy of every answer: scripts may run, in an opaque origin (no allow-same-origin), with nothing to reach: no
 * cookies, no storage of the app, no requests. Inline code and data: URLs are all a published page can use.
 */
export const ARTIFACT_CSP =
  "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; form-action 'none'; base-uri 'none'";

function attachmentName(name: string): string {
  return name.replace(/[^\w.\- ]/g, "_").slice(0, NAME_MAX) || "artifact";
}

/** Response headers for one version. A file the viewer does not show comes back as opaque bytes. */
export function artifactHeaders(v: Pick<VersionRow, "contentType" | "name" | "sha256">, download: boolean): Record<string, string> {
  const type = bareType(v.contentType);
  const kind = artifactKind(type);
  const inline = !download && kind !== "file";
  const served: Record<ArtifactKindName, string> = {
    page: "text/html; charset=utf-8",
    markdown: "text/plain; charset=utf-8",
    text: "text/plain; charset=utf-8",
    image: type,
    pdf: type,
    file: "application/octet-stream",
  };
  const headers: Record<string, string> = {
    "content-type": served[kind],
    "content-disposition": `${inline ? "inline" : "attachment"}; filename="${attachmentName(v.name)}"`,
    "content-security-policy": ARTIFACT_CSP,
    "x-content-type-options": "nosniff",
    "referrer-policy": "no-referrer",
    "cross-origin-resource-policy": "cross-origin",
    // Sharing can be taken back: nothing may keep the bytes past that.
    "cache-control": "private, no-store",
    etag: `"${v.sha256}"`,
  };
  return headers;
}

function checkTitle(title: string): string {
  const t = title.trim();
  if (!t) throw new ApiError("invalid_request", "title is required");
  if (t.length > TITLE_MAX) throw new ApiError("invalid_request", `Titles have at most ${TITLE_MAX} characters`);
  return t;
}

async function versionOf(id: string, number: number): Promise<VersionRow | undefined> {
  const v = schema.artifactVersion;
  return getDb().query.artifactVersion.findFirst({ where: and(eq(v.artifactId, id), eq(v.number, number)) });
}

async function ownerNames(rows: ArtifactRow[], me: string): Promise<Map<string, string>> {
  const ids = [...new Set(rows.map((r) => r.ownerId).filter((id) => id !== me))];
  if (!ids.length) return new Map();
  const u = schema.user;
  const users = await getDb().select({ id: u.id, name: u.name }).from(u).where(inArray(u.id, ids));
  return new Map(users.map((x) => [x.id, x.name]));
}

function view(row: ArtifactRow, version: VersionRow, me: string, names: Map<string, string>): ArtifactView {
  const mine = row.ownerId === me;
  return { row, version, kind: artifactKind(version.contentType), mine, ownerName: mine ? "" : (names.get(row.ownerId) ?? "") };
}

/** The artifact if the caller may see it. Anything else does not exist for them. */
async function readable(p: Principal, id: string): Promise<ArtifactRow> {
  if (!id) throw new ApiError("invalid_request", "id is required");
  await ensureDb();
  const row = await getDb().query.artifact.findFirst({ where: eq(schema.artifact.id, id) });
  if (!row || (row.ownerId !== p.user.id && row.visibility !== "shared")) throw new ApiError("not_found", `Artifact ${id} not found`);
  return row;
}

async function owned(p: Principal, id: string): Promise<ArtifactRow> {
  const row = await readable(p, id);
  // Someone else's shared artifact is visible but not theirs to change.
  if (row.ownerId !== p.user.id) throw new ApiError("permission", "Only the owner can change an artifact");
  return row;
}

export interface PublishInput {
  artifact_id: string;
  title: string;
  sha256: string;
  name: string;
  content_type: string;
  session_id: string;
}

export async function publishArtifact(p: Principal, input: PublishInput): Promise<{ view: ArtifactView; unchanged: boolean }> {
  await ensureDb();
  const owner = p.user.id;
  const name = input.name.trim();
  if (name.length > NAME_MAX) throw new ApiError("invalid_request", `Names have at most ${NAME_MAX} characters`);
  const object = await statObjectOf(owner, input.sha256);
  if (!object) throw new ApiError("failed_precondition", `Object ${input.sha256} is not uploaded`);
  const contentType = artifactType(name, input.content_type || object.contentType);
  const db = getDb();
  const a = schema.artifact;
  const v = schema.artifactVersion;
  const now = new Date();

  const record = () => addObjectRecord(owner, input.sha256, object.size, { session_id: "", kind: "artifact", name, content_type: contentType });

  if (!input.artifact_id) {
    const title = checkTitle(input.title);
    const id = newId("art");
    await record();
    const version: VersionRow = { artifactId: id, number: 1, sha256: input.sha256, name, contentType, size: object.size, createdAt: now };
    const row: ArtifactRow = { id, ownerId: owner, title, visibility: "private", sessionId: input.session_id, currentVersion: 1, createdAt: now, updatedAt: now };
    await db.transaction(async (tx) => {
      await tx.insert(a).values(row);
      await tx.insert(v).values(version);
    });
    return { view: view(row, version, owner, new Map()), unchanged: false };
  }

  const row = await owned(p, input.artifact_id);
  const current = await versionOf(row.id, row.currentVersion);
  const title = input.title.trim() ? checkTitle(input.title) : row.title;
  if (current && current.sha256 === input.sha256 && title === row.title) return { view: view(row, current, owner, new Map()), unchanged: true };
  if (current && current.sha256 === input.sha256) {
    const [renamed] = await db.update(a).set({ title, updatedAt: now }).where(eq(a.id, row.id)).returning();
    return { view: view(renamed!, current, owner, new Map()), unchanged: true };
  }
  await record();
  const number = row.currentVersion + 1;
  const version: VersionRow = { artifactId: row.id, number, sha256: input.sha256, name, contentType, size: object.size, createdAt: now };
  const updated = await db.transaction(async (tx) => {
    await tx.insert(v).values(version);
    const [r] = await tx.update(a).set({ title, currentVersion: number, updatedAt: now }).where(eq(a.id, row.id)).returning();
    return r!;
  });
  return { view: view(updated, version, owner, new Map()), unchanged: false };
}

export interface ListInput {
  filter: "all" | "mine" | "shared";
  query?: string;
  session_id?: string;
  page_size?: number;
  page_token?: string;
}

export async function listArtifacts(p: Principal, input: ListInput): Promise<{ artifacts: ArtifactView[]; next_page_token: string }> {
  await ensureDb();
  const a = schema.artifact;
  const v = schema.artifactVersion;
  const me = p.user.id;
  const size = Math.min(Math.max(input.page_size || 50, 1), 200);
  const offset = Number.parseInt(input.page_token || "0", 10);
  if (!Number.isSafeInteger(offset) || offset < 0) throw new ApiError("invalid_request", "Invalid page_token");
  const scope = {
    mine: eq(a.ownerId, me),
    shared: and(eq(a.visibility, "shared"), ne(a.ownerId, me)),
    all: or(eq(a.ownerId, me), eq(a.visibility, "shared")),
  }[input.filter];
  const conds = [scope];
  const q = input.query?.trim();
  if (q) conds.push(ilike(a.title, `%${q.replace(/[\\%_]/g, "\\$&")}%`));
  // A session id means something to its owner only.
  if (input.session_id) conds.push(and(eq(a.ownerId, me), eq(a.sessionId, input.session_id)));
  const rows = await getDb()
    .select({ a, v })
    .from(a)
    .innerJoin(v, and(eq(v.artifactId, a.id), eq(v.number, a.currentVersion)))
    .where(and(...conds))
    .orderBy(desc(a.updatedAt), desc(a.id))
    .limit(size + 1)
    .offset(offset);
  const page = rows.slice(0, size);
  const names = await ownerNames(page.map((r) => r.a), me);
  return { artifacts: page.map((r) => view(r.a, r.v, me, names)), next_page_token: rows.length > size ? String(offset + size) : "" };
}

export async function getArtifact(p: Principal, id: string, versionNumber = 0): Promise<{ view: ArtifactView; versions: VersionRow[]; text: string }> {
  const row = await readable(p, id);
  const v = schema.artifactVersion;
  const versions = await getDb().select().from(v).where(eq(v.artifactId, id)).orderBy(desc(v.number));
  const current = versions.find((x) => x.number === row.currentVersion);
  if (!current) throw new ApiError("not_found", `Artifact ${id} not found`);
  const shown = versionNumber ? versions.find((x) => x.number === versionNumber) : current;
  if (!shown) throw new ApiError("not_found", `Artifact ${id} has no version ${versionNumber}`);
  let text = "";
  const kind = artifactKind(shown.contentType);
  if ((kind === "markdown" || kind === "text") && shown.size <= TEXT_INLINE_MAX) {
    const bytes = await readObjectBytes(row.ownerId, shown.sha256);
    if (bytes) text = new TextDecoder().decode(bytes);
  }
  const names = await ownerNames([row], p.user.id);
  return { view: view(row, current, p.user.id, names), versions, text };
}

export async function updateArtifact(p: Principal, id: string, patch: { title?: string; visibility?: ArtifactVisibility }): Promise<ArtifactView> {
  const row = await owned(p, id);
  const a = schema.artifact;
  const set: Partial<ArtifactRow> = { updatedAt: new Date() };
  if (patch.title !== undefined) set.title = checkTitle(patch.title);
  if (patch.visibility !== undefined) set.visibility = patch.visibility;
  const [updated] = await getDb().update(a).set(set).where(eq(a.id, row.id)).returning();
  const version = await versionOf(row.id, updated!.currentVersion);
  if (!version) throw new ApiError("not_found", `Artifact ${id} not found`);
  return view(updated!, version, p.user.id, new Map());
}

export async function deleteArtifact(p: Principal, id: string): Promise<void> {
  const row = await owned(p, id);
  const db = getDb();
  const a = schema.artifact;
  const v = schema.artifactVersion;
  const versions = await db.select({ sha256: v.sha256 }).from(v).where(eq(v.artifactId, id));
  await db.delete(a).where(eq(a.id, id));
  // Bytes another artifact of the owner's still shows stay.
  const shas = [...new Set(versions.map((x) => x.sha256))];
  const others = shas.length
    ? await db
        .selectDistinct({ sha256: v.sha256 })
        .from(v)
        .innerJoin(a, eq(a.id, v.artifactId))
        .where(and(eq(a.ownerId, row.ownerId), inArray(v.sha256, shas)))
    : [];
  const kept = new Set(others.map((x) => x.sha256));
  await deleteObjectRecords(row.ownerId, "artifact", shas.filter((s) => !kept.has(s)));
}

/** Answers GET and HEAD /api/artifacts/<id>/content. */
export async function serveArtifact(p: Principal, id: string, opts: { version: number; download: boolean; method: string; ifNoneMatch: string | null }): Promise<Response> {
  const row = await readable(p, id);
  const version = await versionOf(row.id, opts.version || row.currentVersion);
  if (!version) throw new ApiError("not_found", `Artifact ${id} has no version ${opts.version}`);
  const headers = artifactHeaders(version, opts.download);
  if (opts.ifNoneMatch === headers.etag) return new Response(null, { status: 304, headers });
  headers["content-length"] = String(version.size);
  if (opts.method === "HEAD" || version.size === 0) return new Response(null, { status: 200, headers });
  const body = await openObject(row.ownerId, version.sha256);
  if (!body) throw new ApiError("not_found", `Artifact ${id} has lost its bytes`);
  return new Response(body, { status: 200, headers });
}

/** Number of artifacts of a user, for tests and reports. */
export async function countArtifacts(owner: string): Promise<number> {
  await ensureDb();
  const a = schema.artifact;
  const [r] = await getDb().select({ n: sql<number>`count(*)::int` }).from(a).where(eq(a.ownerId, owner));
  return r?.n ?? 0;
}
