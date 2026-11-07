/**
 * Knowledge: text documents on the server, searched by agents with the KnowledgeSearch tool and managed on its
 * screen. Scope "org" is written by admins and read by everyone. Scope "user" belongs to its owner, and no role reaches
 * it: an admin sees someone's personal documents exactly as much as anyone else, not at all.
 *
 * A document is split into passages when written. Search is Postgres full text on the passages, with two configurations
 * in one vector: 'english' stems, 'simple' keeps words of every other language intact. Any word of the query may match;
 * passages matching more of them, or in the title, rank higher.
 *
 * Provider sources are read live through admin connections and never stored: a tracker's articles for everyone, ids
 * conn:<connection id>:<article id>. Sources on the user's device are the client's, which merges them with this.
 */
import { and, asc, desc, eq, ilike, or, sql, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { getConnection, listConnections, openProvider, providerMessage, type ConnectionRow } from "../tracker/connections.ts";
import { freeTextTerms, snippetOf, type Article, type TrackerProvider } from "../tracker/provider.ts";
import type { Principal } from "./devices.ts";

export type KnowledgeScope = "user" | "org";
type DocumentRow = typeof schema.knowledgeDocument.$inferSelect;

export interface SourceRef {
  id: string;
  kind: "internal" | "provider";
  scope: KnowledgeScope;
  name: string;
  provider: string;
}

export interface KnowledgeDocumentView {
  row: DocumentRow;
  editable: boolean;
  source: SourceRef;
  url: string;
  hasChildren: boolean;
}

export interface KnowledgeHit {
  document_id: string;
  title: string;
  scope: KnowledgeScope;
  snippet: string;
  score: number;
  source: SourceRef;
  url: string;
}

const INTERNAL_ORG: SourceRef = { id: "internal:org", kind: "internal", scope: "org", name: "Internal, for everyone", provider: "" };
const INTERNAL_USER: SourceRef = { id: "internal:user", kind: "internal", scope: "user", name: "Internal, only you", provider: "" };
const internalSource = (scope: string): SourceRef => (scope === "org" ? INTERNAL_ORG : INTERNAL_USER);
const providerSource = (c: ConnectionRow): SourceRef => ({ id: c.id, kind: "provider", scope: "org", name: c.name, provider: c.provider });

function documentView(row: DocumentRow, p: Principal): KnowledgeDocumentView {
  return { row, editable: canEdit(row, p), source: internalSource(row.scope), url: "", hasChildren: false };
}

const TITLE_MAX = 200;
/** Fits through the client's proxy, which forwards at most 1 MiB. */
export const KNOWLEDGE_MAX_BYTES = 900 << 10;
const CHUNK_CHARS = 1200;
const CHUNK_OVERLAP = 150;
const SEARCH_MAX_WORDS = 32;

/** Words that say nothing about what a passage is about, in the languages the 'simple' configuration would keep them for. */
const STOP_WORDS = new Set(
  "a an and are as at be but by for from has have how i in is it its of on or that the this to was were what when where which who why will with you your и в во не что он на я с со как а то все она так его но да ты к у же вы за бы по только ее мне было вот от меня еще нет о из ему где есть для это".split(" "),
);

function canEdit(row: Pick<DocumentRow, "scope" | "ownerId">, p: Principal): boolean {
  return row.scope === "org" ? p.user.role === "admin" : row.ownerId === p.user.id;
}

function readableBy(p: Principal, scope?: KnowledgeScope): SQL {
  const d = schema.knowledgeDocument;
  const mine = and(eq(d.scope, "user"), eq(d.ownerId, p.user.id))!;
  const org = eq(d.scope, "org");
  if (scope === "user") return mine;
  if (scope === "org") return org;
  return or(mine, org)!;
}

async function readable(p: Principal, id: string): Promise<DocumentRow> {
  if (!id) throw new ApiError("invalid_request", "id is required");
  await ensureDb();
  const d = schema.knowledgeDocument;
  const row = await getDb().query.knowledgeDocument.findFirst({ where: and(eq(d.id, id), readableBy(p)) });
  if (!row) throw new ApiError("not_found", `Document ${id} not found`);
  return row;
}

function checkTitle(title: string): string {
  const t = title.trim();
  if (!t) throw new ApiError("invalid_request", "title is required");
  if (t.length > TITLE_MAX) throw new ApiError("invalid_request", `Titles have at most ${TITLE_MAX} characters`);
  return t;
}

function checkContent(content: string): number {
  if (!content.trim()) throw new ApiError("invalid_request", "content is required");
  if (content.includes("\0")) throw new ApiError("invalid_request", "Only text documents can be added");
  const size = Buffer.byteLength(content);
  if (size > KNOWLEDGE_MAX_BYTES) throw new ApiError("invalid_request", `Documents have at most ${KNOWLEDGE_MAX_BYTES} bytes`);
  return size;
}

/** Splits on paragraphs where it can, so a passage reads as one; a paragraph longer than a passage is cut with an overlap. */
export function chunkText(content: string): string[] {
  const out: string[] = [];
  let cur = "";
  const flush = () => {
    if (cur.trim()) out.push(cur.trim());
    cur = "";
  };
  for (const para of content.replace(/\r\n/g, "\n").split(/\n\s*\n/)) {
    if (cur.length + para.length + 2 > CHUNK_CHARS) flush();
    if (para.length <= CHUNK_CHARS) {
      cur = cur ? `${cur}\n\n${para}` : para;
      continue;
    }
    for (let i = 0; i < para.length; i += CHUNK_CHARS - CHUNK_OVERLAP) {
      out.push(para.slice(i, i + CHUNK_CHARS).trim());
      if (i + CHUNK_CHARS >= para.length) break;
    }
  }
  flush();
  return out.filter(Boolean);
}

type Tx = Parameters<Parameters<ReturnType<typeof getDb>["transaction"]>[0]>[0];

async function writeChunks(tx: Tx, id: string, title: string, content: string): Promise<void> {
  const c = schema.knowledgeChunk;
  await tx.delete(c).where(eq(c.documentId, id));
  const values = chunkText(content).map((text, index) => ({ documentId: id, index, heading: title, text }));
  for (let i = 0; i < values.length; i += 100) await tx.insert(c).values(values.slice(i, i + 100));
}

export async function createKnowledgeDocument(p: Principal, input: { scope: KnowledgeScope; title: string; content: string; source_name?: string }): Promise<KnowledgeDocumentView> {
  await ensureDb();
  if (input.scope === "org" && p.user.role !== "admin") throw new ApiError("permission", "Only admins add documents for everyone");
  const title = checkTitle(input.title);
  const size = checkContent(input.content);
  const now = new Date();
  const row: DocumentRow = { id: newId("kb"), scope: input.scope, ownerId: p.user.id, title, sourceName: (input.source_name ?? "").slice(0, 255), content: input.content, size, createdAt: now, updatedAt: now };
  await getDb().transaction(async (tx) => {
    await tx.insert(schema.knowledgeDocument).values(row);
    await writeChunks(tx, row.id, title, input.content);
  });
  return documentView(row, p);
}

export async function listKnowledgeDocuments(p: Principal, input: { scope?: KnowledgeScope; query?: string }): Promise<KnowledgeDocumentView[]> {
  await ensureDb();
  const d = schema.knowledgeDocument;
  const conds = [readableBy(p, input.scope)];
  const q = input.query?.trim();
  if (q) conds.push(ilike(d.title, `%${q.replace(/[\\%_]/g, "\\$&")}%`));
  const rows = await getDb().select().from(d).where(and(...conds)).orderBy(asc(d.scope), desc(d.updatedAt)).limit(500);
  return rows.map((row) => documentView(row, p));
}

export async function getKnowledgeDocument(p: Principal, id: string): Promise<KnowledgeDocumentView> {
  const ref = parseProviderId(id);
  if (ref) return readProviderDocument(ref.connectionId, ref.articleId);
  return documentView(await readable(p, id), p);
}

async function writable(p: Principal, id: string): Promise<DocumentRow> {
  const row = await readable(p, id);
  if (!canEdit(row, p)) throw new ApiError("permission", "Only admins change documents shared with everyone");
  return row;
}

export async function updateKnowledgeDocument(p: Principal, id: string, patch: { title?: string; content?: string }): Promise<KnowledgeDocumentView> {
  const row = await writable(p, id);
  const title = patch.title !== undefined ? checkTitle(patch.title) : row.title;
  const content = patch.content ?? row.content;
  const size = checkContent(content);
  const d = schema.knowledgeDocument;
  const updated = await getDb().transaction(async (tx) => {
    const [r] = await tx.update(d).set({ title, content, size, updatedAt: new Date() }).where(eq(d.id, id)).returning();
    await writeChunks(tx, id, title, content);
    return r!;
  });
  return documentView(updated, p);
}

export async function deleteKnowledgeDocument(p: Principal, id: string): Promise<void> {
  await writable(p, id);
  await getDb().delete(schema.knowledgeDocument).where(eq(schema.knowledgeDocument.id, id));
}

/** The words of a query as tsquery alternatives, quoted so nothing in them is an operator. "" when nothing is left. */
export function orQuery(text: string): string {
  const words = text.toLowerCase().match(/[\p{L}\p{N}][\p{L}\p{N}_.-]*/gu) ?? [];
  const useful = words.filter((w) => !STOP_WORDS.has(w));
  const kept = (useful.length ? useful : words).slice(0, SEARCH_MAX_WORDS);
  return [...new Set(kept)].map((w) => `'${w.replace(/'/g, "")}'`).join(" | ");
}

async function searchInternal(p: Principal, input: { query: string; limit?: number; scope?: KnowledgeScope }): Promise<KnowledgeHit[]> {
  const tsq = orQuery(input.query);
  if (!tsq) throw new ApiError("invalid_request", "query is required");
  const limit = Math.min(Math.max(input.limit || 8, 1), 30);
  const d = schema.knowledgeDocument;
  const c = schema.knowledgeChunk;
  const q = sql`(to_tsquery('english', ${tsq}) || to_tsquery('simple', ${tsq}))`;
  const score = sql<number>`ts_rank(${c.search}, ${q}, 1)`;
  const rows = await getDb()
    .select({ documentId: d.id, title: d.title, scope: d.scope, text: c.text, score })
    .from(c)
    .innerJoin(d, eq(d.id, c.documentId))
    .where(and(readableBy(p, input.scope), sql`${c.search} @@ ${q}`))
    .orderBy(desc(score), asc(d.id), asc(c.index))
    .limit(limit * 4);
  // A long document does not get to fill the answer on its own.
  const perDoc = new Map<string, number>();
  const hits: KnowledgeHit[] = [];
  for (const r of rows) {
    const n = perDoc.get(r.documentId) ?? 0;
    if (n >= 2) continue;
    perDoc.set(r.documentId, n + 1);
    hits.push({ document_id: r.documentId, title: r.title, scope: r.scope as KnowledgeScope, snippet: r.text, score: Number(r.score), source: internalSource(r.scope), url: "" });
    if (hits.length >= limit) break;
  }
  return hits;
}

// ---- provider sources, read live ----

const PROVIDER_ID = /^conn:([^:]+):(.+)$/;

export function parseProviderId(id: string): { connectionId: string; articleId: string } | undefined {
  const m = PROVIDER_ID.exec(id);
  return m ? { connectionId: m[1]!, articleId: m[2]! } : undefined;
}

const providerDocumentId = (connectionId: string, articleId: string) => `conn:${connectionId}:${articleId}`;

async function sourceConnections(): Promise<ConnectionRow[]> {
  return (await listConnections()).filter((c) => c.enabled && c.knowledgeEnabled);
}

function articleProvider(conn: ConnectionRow): TrackerProvider {
  const provider = openProvider(conn);
  if (!provider.capabilities().knowledge || !provider.searchArticles || !provider.listArticles || !provider.getArticle) throw new ApiError("failed_precondition", `${conn.name} has no documents`);
  return provider;
}

function articleView(conn: ConnectionRow, a: Article): KnowledgeDocumentView {
  const updated = a.updatedAt ? new Date(a.updatedAt) : new Date(0);
  return {
    row: {
      id: providerDocumentId(conn.id, a.id),
      scope: "org",
      ownerId: "",
      title: a.title || a.id,
      sourceName: a.project,
      content: a.content,
      size: Buffer.byteLength(a.content),
      createdAt: updated,
      updatedAt: updated,
    },
    editable: false,
    source: providerSource(conn),
    url: a.url,
    hasChildren: a.hasChildren,
  };
}

/** A source that is switched off does not exist for readers, so an old id can't get around that. */
async function usableSource(connectionId: string): Promise<ConnectionRow> {
  const conn = await getConnection(connectionId);
  if (!conn?.enabled || !conn.knowledgeEnabled) throw new ApiError("not_found", "This source is not available");
  return conn;
}

async function readProviderDocument(connectionId: string, articleId: string): Promise<KnowledgeDocumentView> {
  const conn = await usableSource(connectionId);
  try {
    return articleView(conn, await articleProvider(conn).getArticle!(articleId));
  } catch (err) {
    if (err instanceof ApiError) throw err;
    throw new ApiError("upstream", providerMessage(err));
  }
}

export interface KnowledgeWarning {
  source: SourceRef;
  message: string;
}

/** Internal passages and each provider source's own order, taken in turn: their scores can't be compared. */
function alternate(lists: KnowledgeHit[][], limit: number): KnowledgeHit[] {
  const out: KnowledgeHit[] = [];
  for (let i = 0; out.length < limit; i++) {
    let any = false;
    for (const list of lists) {
      if (i < list.length && out.length < limit) {
        out.push(list[i]!);
        any = true;
      }
    }
    if (!any) break;
  }
  return out;
}

export async function searchKnowledge(p: Principal, input: { query: string; limit?: number; scope?: KnowledgeScope }): Promise<{ hits: KnowledgeHit[]; warnings: KnowledgeWarning[] }> {
  await ensureDb();
  const limit = Math.min(Math.max(input.limit || 8, 1), 30);
  const internal = await searchInternal(p, { ...input, limit });
  const warnings: KnowledgeWarning[] = [];
  const lists: KnowledgeHit[][] = [internal];
  if (input.scope !== "user") {
    const terms = freeTextTerms(input.query);
    const results = await Promise.all(
      (await sourceConnections()).map(async (conn) => {
        try {
          const articles = await articleProvider(conn).searchArticles!(input.query, conn.knowledgeQuery, limit);
          return articles.map(
            (a): KnowledgeHit => ({ document_id: providerDocumentId(conn.id, a.id), title: a.title || a.id, scope: "org", snippet: snippetOf(a.content, terms), score: 0, source: providerSource(conn), url: a.url }),
          );
        } catch (err) {
          // Why it failed can name an internal host: admins are told, others only that it did.
          warnings.push({ source: providerSource(conn), message: p.user.role === "admin" ? providerMessage(err) : "This source could not be searched right now." });
          return [];
        }
      }),
    );
    lists.push(...results);
  }
  return { hits: alternate(lists, limit), warnings };
}

export interface KnowledgeSourceView {
  ref: SourceRef;
  documentCount: number;
  writable: boolean;
}

export async function listKnowledgeSources(p: Principal): Promise<KnowledgeSourceView[]> {
  await ensureDb();
  const d = schema.knowledgeDocument;
  const count = async (scope?: KnowledgeScope) => (await getDb().select({ n: sql<number>`count(*)` }).from(d).where(readableBy(p, scope)))[0]?.n ?? 0;
  const out: KnowledgeSourceView[] = [
    { ref: INTERNAL_ORG, documentCount: Number(await count("org")), writable: p.user.role === "admin" },
    { ref: INTERNAL_USER, documentCount: Number(await count("user")), writable: true },
  ];
  for (const c of await sourceConnections()) out.push({ ref: providerSource(c), documentCount: 0, writable: false });
  return out;
}

export async function browseKnowledgeSource(p: Principal, input: { sourceId: string; parentId?: string; limit?: number; offset?: number }): Promise<{ documents: KnowledgeDocumentView[]; hasMore: boolean }> {
  await ensureDb();
  if (input.sourceId === INTERNAL_ORG.id || input.sourceId === INTERNAL_USER.id) {
    return { documents: await listKnowledgeDocuments(p, { scope: input.sourceId === INTERNAL_ORG.id ? "org" : "user" }), hasMore: false };
  }
  const conn = await usableSource(input.sourceId);
  const limit = Math.min(Math.max(input.limit || 100, 1), 200);
  try {
    const r = await articleProvider(conn).listArticles!(input.parentId ?? "", conn.knowledgeQuery, { limit, offset: input.offset ?? 0 });
    return { documents: r.articles.map((a) => articleView(conn, a)), hasMore: r.hasMore };
  } catch (err) {
    if (err instanceof ApiError) throw err;
    throw new ApiError("upstream", providerMessage(err));
  }
}
