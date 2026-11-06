/**
 * Projects: what boards and issues belong to. Internal, or mapped onto a provider project with local values over the provider's.
 * Personal or global, and shared with users, groups, roles or everyone at view, edit or manage. Enforced here: every other
 * service asks `levels` what the caller holds. docs/design/tracker.md.
 *
 * The owner of a personal project holds manage. An admin holds manage on global projects, and on another user's personal project
 * only what someone shared with them, as any user would.
 */
import { and, asc, eq, inArray, or, sql, type SQL } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { connectionsById, openProvider, providerMessage, requireConnection } from "../tracker/connections.ts";
import type { Principal } from "./devices.ts";
import { groupsOfUser } from "./groups.ts";

export type ProjectRow = typeof schema.project.$inferSelect;
export type ShareRow = typeof schema.projectShare.$inferSelect;
export type Level = "view" | "edit" | "manage";
export type ShareSubject = "user" | "group" | "role" | "everyone";

export interface Metadata {
  ticker: string;
  name: string;
  description: string;
  logo: string;
  color: string;
}

export interface ProjectView {
  row: ProjectRow;
  metadata: Metadata;
  local: Metadata;
  provider: Metadata;
  level: Level;
  shares: (ShareRow & { subjectName: string })[];
}

const RANK: Record<Level, number> = { view: 1, edit: 2, manage: 3 };
const TICKER = /^[A-Z][A-Z0-9]{1,9}$/;
const COLOR = /^#[0-9a-fA-F]{6}$/;
const NAME_MAX = 200;
const DESCRIPTION_MAX = 8 << 10;
const LOGO_MAX = 2000;
const MAX_SHARES = 200;

export function atLeast(have: Level | undefined, need: Level): boolean {
  return !!have && RANK[have] >= RANK[need];
}

/** A provider's short name as a ticker: DEMO stays, a name that can't be one is cut down to what can. */
export function tickerFrom(raw: string): string {
  const t = raw.toUpperCase().replace(/[^A-Z0-9]/g, "");
  const start = t.replace(/^[0-9]+/, "");
  const cut = start.slice(0, 10);
  return cut.length >= 2 ? cut : `${cut || "P"}X`.slice(0, 10);
}

function metadataOf(row: ProjectRow): { metadata: Metadata; local: Metadata; provider: Metadata } {
  const prov = (row.providerValues ?? {}) as Partial<Metadata>;
  const provider: Metadata = { ticker: prov.ticker ?? "", name: prov.name ?? "", description: prov.description ?? "", logo: prov.logo ?? "", color: prov.color ?? "" };
  const local: Metadata = { ticker: row.ticker ?? "", name: row.name ?? "", description: row.description ?? "", logo: row.logo ?? "", color: row.color ?? "" };
  return {
    local,
    provider,
    metadata: {
      ticker: local.ticker || tickerFrom(provider.ticker || row.externalKey || row.id),
      name: local.name || provider.name || row.externalKey,
      description: local.description || provider.description,
      logo: local.logo || provider.logo,
      color: local.color || provider.color,
    },
  };
}

export const tickerOf = (row: ProjectRow): string => metadataOf(row).metadata.ticker;

/**
 * Every project the caller holds a level on, with that level. Shares are fetched by subject, so a project nobody shared with
 * them is never loaded at all.
 */
export async function levels(p: Principal): Promise<Map<string, Level>> {
  await ensureDb();
  const db = getDb();
  const s = schema.projectShare;
  const pr = schema.project;
  const out = new Map<string, Level>();
  const give = (id: string, level: Level) => {
    const cur = out.get(id);
    if (!cur || RANK[level] > RANK[cur]) out.set(id, level);
  };
  const owned: SQL = p.user.role === "admin" ? or(eq(pr.ownerId, p.user.id), eq(pr.scope, "global"))! : eq(pr.ownerId, p.user.id);
  for (const r of await db.select({ id: pr.id }).from(pr).where(owned)) give(r.id, "manage");
  const groups = (await groupsOfUser(p.user.id)).map((g) => g.id);
  const shares = await db
    .select({ projectId: s.projectId, level: s.level })
    .from(s)
    .where(
      or(
        eq(s.subject, "everyone"),
        and(eq(s.subject, "user"), eq(s.subjectId, p.user.id)),
        and(eq(s.subject, "role"), eq(s.subjectId, p.user.role)),
        groups.length ? and(eq(s.subject, "group"), inArray(s.subjectId, groups)) : undefined,
      ),
    );
  for (const r of shares) give(r.projectId, r.level as Level);
  return out;
}

export async function levelOn(p: Principal, projectId: string): Promise<Level | undefined> {
  return (await levels(p)).get(projectId);
}

async function shareNames(shares: ShareRow[]): Promise<Map<string, string>> {
  const db = getDb();
  const out = new Map<string, string>();
  const users = shares.filter((x) => x.subject === "user").map((x) => x.subjectId);
  if (users.length) {
    const u = schema.user;
    for (const r of await db.select({ id: u.id, name: u.name, email: u.email }).from(u).where(inArray(u.id, users))) out.set(`user:${r.id}`, r.name || r.email);
  }
  const groups = shares.filter((x) => x.subject === "group").map((x) => x.subjectId);
  if (groups.length) {
    const g = schema.directoryGroup;
    for (const r of await db.select({ id: g.id, name: g.name }).from(g).where(inArray(g.id, groups))) out.set(`group:${r.id}`, r.name);
  }
  return out;
}

async function viewOf(row: ProjectRow, level: Level): Promise<ProjectView> {
  const s = schema.projectShare;
  // Who else it is shared with is the manager's matter.
  const shares = level === "manage" ? await getDb().select().from(s).where(eq(s.projectId, row.id)) : [];
  const names = await shareNames(shares);
  return {
    row,
    level,
    ...metadataOf(row),
    shares: shares.map((x) => ({ ...x, subjectName: x.subject === "everyone" ? "Everyone" : x.subject === "role" ? x.subjectId : (names.get(`${x.subject}:${x.subjectId}`) ?? x.subjectId) })),
  };
}

export async function listProjects(p: Principal): Promise<ProjectView[]> {
  const lv = await levels(p);
  if (!lv.size) return [];
  const pr = schema.project;
  const rows = await getDb().select().from(pr).where(inArray(pr.id, [...lv.keys()])).orderBy(asc(pr.createdAt));
  return Promise.all(rows.map((r) => viewOf(r, lv.get(r.id)!)));
}

/** By id or by ticker. Of two with one ticker, the caller's own personal project wins. */
export async function findProject(p: Principal, idOrTicker: string): Promise<{ row: ProjectRow; level: Level }> {
  if (!idOrTicker) throw new ApiError("invalid_request", "A project is required");
  const lv = await levels(p);
  const pr = schema.project;
  const byId = lv.has(idOrTicker) ? await getDb().query.project.findFirst({ where: eq(pr.id, idOrTicker) }) : undefined;
  if (byId) return { row: byId, level: lv.get(byId.id)! };
  const wanted = idOrTicker.toUpperCase();
  const rows = lv.size ? await getDb().select().from(pr).where(inArray(pr.id, [...lv.keys()])) : [];
  const found = rows.filter((r) => tickerOf(r) === wanted).sort((a) => (a.ownerId === p.user.id ? -1 : 1))[0];
  if (!found) throw new ApiError("not_found", `Project ${idOrTicker} not found`);
  return { row: found, level: lv.get(found.id)! };
}

export async function getProject(p: Principal, id: string): Promise<ProjectView> {
  const { row, level } = await findProject(p, id);
  return viewOf(row, level);
}

function checkMetadata(m: Partial<Metadata>): void {
  if (m.ticker && !TICKER.test(m.ticker)) throw new ApiError("invalid_request", "A ticker is 2 to 10 upper case letters and digits, starting with a letter");
  if (m.name && m.name.length > NAME_MAX) throw new ApiError("invalid_request", `Names have at most ${NAME_MAX} characters`);
  if (m.description && m.description.length > DESCRIPTION_MAX) throw new ApiError("invalid_request", `Descriptions have at most ${DESCRIPTION_MAX} characters`);
  if (m.logo && m.logo.length > LOGO_MAX) throw new ApiError("invalid_request", `A logo is at most ${LOGO_MAX} characters`);
  if (m.logo && /^[a-z]+:/i.test(m.logo) && !/^https?:\/\//i.test(m.logo)) throw new ApiError("invalid_request", "A logo is an http(s) image URL or an emoji");
  if (m.color && !COLOR.test(m.color)) throw new ApiError("invalid_request", "A color is #rrggbb");
}

/** Tickers are unique among global projects, and among one user's personal projects. */
async function checkTickerFree(ticker: string, scope: string, ownerId: string | null, self?: string): Promise<void> {
  const pr = schema.project;
  const rows = await getDb()
    .select()
    .from(pr)
    .where(scope === "global" ? eq(pr.scope, "global") : and(eq(pr.scope, "personal"), eq(pr.ownerId, ownerId ?? "")));
  if (rows.some((r) => r.id !== self && tickerOf(r) === ticker)) throw new ApiError("failed_precondition", `The ticker ${ticker} is already taken`);
}

async function checkShares(shares: { subject: ShareSubject; subjectId: string; level: Level }[]): Promise<void> {
  if (shares.length > MAX_SHARES) throw new ApiError("invalid_request", `At most ${MAX_SHARES} shares`);
  const db = getDb();
  const users = shares.filter((x) => x.subject === "user").map((x) => x.subjectId);
  if (users.length) {
    const u = schema.user;
    const found = new Set((await db.select({ id: u.id }).from(u).where(inArray(u.id, users))).map((r) => r.id));
    for (const id of users) if (!found.has(id)) throw new ApiError("invalid_request", `No user ${id}`);
  }
  const groups = shares.filter((x) => x.subject === "group").map((x) => x.subjectId);
  if (groups.length) {
    const g = schema.directoryGroup;
    const found = new Set((await db.select({ id: g.id }).from(g).where(inArray(g.id, groups))).map((r) => r.id));
    for (const id of groups) if (!found.has(id)) throw new ApiError("invalid_request", `No group ${id}`);
  }
  for (const s of shares) {
    if (s.subject === "role" && s.subjectId !== "admin" && s.subjectId !== "user") throw new ApiError("invalid_request", "A role is admin or user");
    if (s.subject === "everyone" && s.subjectId) throw new ApiError("invalid_request", "A share with everyone names nobody");
  }
}

export async function createProject(
  p: Principal,
  input: {
    scope: "personal" | "global";
    metadata: Partial<Metadata>;
    connection?: { scope: "admin" | "device"; id: string };
    externalKey?: string;
    provider?: Partial<Metadata>;
    shares?: { subject: ShareSubject; subjectId: string; level: Level }[];
  },
): Promise<ProjectView> {
  await ensureDb();
  if (input.scope === "global" && p.user.role !== "admin") throw new ApiError("permission", "Only admins create global projects");
  if (input.connection?.scope === "device" && input.scope !== "personal") throw new ApiError("invalid_request", "A project on a connection of your device can only be personal");
  if (input.connection && !input.externalKey) throw new ApiError("invalid_request", "Choose the provider's project");
  if (input.connection?.scope === "admin" && !(await connectionsById([input.connection.id])).has(input.connection.id)) throw new ApiError("invalid_request", `No connection ${input.connection.id}`);
  checkMetadata(input.metadata);
  const now = new Date();
  const row: ProjectRow = {
    id: newId("prj"),
    scope: input.scope,
    ownerId: input.scope === "personal" ? p.user.id : null,
    ticker: input.metadata.ticker || null,
    name: input.metadata.name?.trim() || null,
    description: input.metadata.description || null,
    logo: input.metadata.logo || null,
    color: input.metadata.color || null,
    connectionScope: input.connection?.scope ?? "",
    connectionId: input.connection?.id ?? "",
    externalKey: input.externalKey ?? "",
    providerValues: input.provider ?? {},
    nextNumber: 1,
    createdAt: now,
    updatedAt: now,
  };
  const eff = metadataOf(row).metadata;
  if (!eff.name) throw new ApiError("invalid_request", "A project needs a name");
  if (!input.metadata.ticker && !input.connection) throw new ApiError("invalid_request", "A project needs a ticker");
  await checkTickerFree(eff.ticker, row.scope, row.ownerId);
  const shares = input.shares ?? (input.scope === "global" ? [{ subject: "everyone" as const, subjectId: "", level: "edit" as const }] : []);
  await checkShares(shares);
  await getDb().transaction(async (tx) => {
    await tx.insert(schema.project).values(row);
    if (shares.length) await tx.insert(schema.projectShare).values(shares.map((s) => ({ projectId: row.id, subject: s.subject, subjectId: s.subjectId, level: s.level })));
  });
  return viewOf(row, "manage");
}

export async function updateProject(p: Principal, id: string, patch: Partial<Metadata> & { providerValues?: Partial<Metadata> }): Promise<ProjectView> {
  const { row, level } = await findProject(p, id);
  if (!atLeast(level, "manage")) throw new ApiError("permission", "You need manage on this project");
  const { providerValues, ...local } = patch;
  checkMetadata(local);
  const set: Partial<ProjectRow> = { updatedAt: new Date() };
  for (const k of ["ticker", "name", "description", "logo", "color"] as const) {
    if (local[k] !== undefined) set[k] = local[k] === "" ? null : local[k]!;
  }
  if (providerValues && row.connectionId) set.providerValues = providerValues;
  const next = { ...row, ...set } as ProjectRow;
  const eff = metadataOf(next).metadata;
  if (!eff.name) throw new ApiError("invalid_request", "A project needs a name");
  if (!row.connectionId && !next.ticker) throw new ApiError("invalid_request", "A project needs a ticker");
  if (eff.ticker !== tickerOf(row)) await checkTickerFree(eff.ticker, row.scope, row.ownerId, row.id);
  const pr = schema.project;
  const [updated] = await getDb().update(pr).set(set).where(eq(pr.id, row.id)).returning();
  return viewOf(updated!, level);
}

export async function deleteProject(p: Principal, id: string): Promise<void> {
  const { row, level } = await findProject(p, id);
  if (!atLeast(level, "manage")) throw new ApiError("permission", "You need manage on this project");
  await getDb().transaction(async (tx) => {
    // Cards reference stages without a cascade of their own, so they go first.
    await tx.delete(schema.boardCard).where(eq(schema.boardCard.projectId, row.id));
    await tx.delete(schema.project).where(eq(schema.project.id, row.id));
  });
}

export async function setProjectShares(p: Principal, id: string, shares: { subject: ShareSubject; subjectId: string; level: Level }[]): Promise<ProjectView> {
  const { row, level } = await findProject(p, id);
  if (!atLeast(level, "manage")) throw new ApiError("permission", "You need manage on this project");
  const unique = new Map(shares.map((s) => [`${s.subject}:${s.subjectId}`, s]));
  const list = [...unique.values()];
  await checkShares(list);
  const s = schema.projectShare;
  await getDb().transaction(async (tx) => {
    await tx.delete(s).where(eq(s.projectId, row.id));
    if (list.length) await tx.insert(s).values(list.map((x) => ({ projectId: row.id, subject: x.subject, subjectId: x.subjectId, level: x.level })));
  });
  return viewOf(row, level);
}

/** Numbers never come back: a deleted card's number stays used. */
export async function nextCardNumber(tx: { update: ReturnType<typeof getDb>["update"] }, projectId: string): Promise<number> {
  const pr = schema.project;
  const [r] = await tx
    .update(pr)
    .set({ nextNumber: sql`${pr.nextNumber} + 1` })
    .where(eq(pr.id, projectId))
    .returning({ next: pr.nextNumber });
  return r!.next - 1;
}

/** The provider's projects through an admin connection. */
export async function listProviderProjects(connectionId: string): Promise<{ key: string; metadata: Metadata }[]> {
  const conn = await requireConnection(connectionId);
  if (!conn.enabled) throw new ApiError("failed_precondition", `${conn.name} is disabled`);
  try {
    const list = await openProvider(conn).projects();
    return list.map((x) => ({ key: x.key, metadata: { ticker: x.key, name: x.name, description: x.description, logo: x.logo, color: "" } }));
  } catch (err) {
    throw new ApiError("upstream", providerMessage(err));
  }
}

export interface Member {
  userId: string;
  name: string;
  email: string;
  level: Level;
}

/** Who holds a level on a project: who can be assigned. */
export async function listProjectMembers(p: Principal, id: string): Promise<Member[]> {
  const { row } = await findProject(p, id);
  const db = getDb();
  const u = schema.user;
  const s = schema.projectShare;
  const m = schema.directoryGroupMember;
  const shares = await db.select().from(s).where(eq(s.projectId, row.id));
  const out = new Map<string, Level>();
  const give = (uid: string, level: Level) => {
    const cur = out.get(uid);
    if (!cur || RANK[level] > RANK[cur]) out.set(uid, level);
  };
  if (row.ownerId) give(row.ownerId, "manage");
  for (const x of shares.filter((r) => r.subject === "user")) give(x.subjectId, x.level as Level);
  const groupShares = shares.filter((r) => r.subject === "group");
  if (groupShares.length) {
    const members = await db.select().from(m).where(inArray(m.groupId, groupShares.map((g) => g.subjectId)));
    for (const x of members) give(x.userId, groupShares.find((g) => g.subjectId === x.groupId)!.level as Level);
  }
  const roleShares = shares.filter((r) => r.subject === "role");
  const everyone = shares.find((r) => r.subject === "everyone");
  const all = everyone || roleShares.length || row.scope === "global" ? await db.select({ id: u.id, role: u.role }).from(u).limit(2000) : [];
  for (const x of all) {
    if (everyone) give(x.id, everyone.level as Level);
    const byRole = roleShares.find((r) => r.subjectId === (x.role ?? "user"));
    if (byRole) give(x.id, byRole.level as Level);
    if (row.scope === "global" && x.role === "admin") give(x.id, "manage");
  }
  if (!out.size) return [];
  const users = await db.select({ id: u.id, name: u.name, email: u.email }).from(u).where(inArray(u.id, [...out.keys()]));
  return users.map((x) => ({ userId: x.id, name: x.name, email: x.email, level: out.get(x.id)! })).sort((a, b) => a.name.localeCompare(b.name));
}

/** The caller's first project with a personal board, when they have no project of their own. */
export async function ensureDefaultProject(p: Principal): Promise<ProjectView> {
  await ensureDb();
  const pr = schema.project;
  const [mine] = await getDb()
    .select()
    .from(pr)
    .where(and(eq(pr.scope, "personal"), eq(pr.ownerId, p.user.id)))
    .orderBy(asc(pr.createdAt))
    .limit(1);
  if (mine) return viewOf(mine, "manage");
  return createProject(p, { scope: "personal", metadata: { ticker: "MY", name: "My issues" } });
}
