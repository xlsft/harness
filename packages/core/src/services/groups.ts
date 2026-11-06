/**
 * Groups of users: quotas apply per group, and directories feed them. A group's `source` says who owns it: local ones are
 * edited in Settings, the others follow SCIM, or the groups an identity provider or LDAP directory reports at sign-in.
 */
import { and, asc, eq, inArray } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";

export type GroupSource = "local" | "scim" | "sso" | "ldap";

export interface GroupView {
  id: string;
  name: string;
  source: string;
  member_ids: string[];
}

const g = schema.directoryGroup;
const m = schema.directoryGroupMember;
type GroupRow = typeof g.$inferSelect;

function cleanName(name: string): string {
  const n = name.trim();
  if (!n || n.length > 256) throw new ApiError("invalid_request", "A group name has 1 to 256 characters");
  return n;
}

async function view(rows: GroupRow[]): Promise<GroupView[]> {
  if (!rows.length) return [];
  const members = await getDb().select().from(m).where(inArray(m.groupId, rows.map((r) => r.id)));
  return rows.map((r) => ({ id: r.id, name: r.name, source: r.source, member_ids: members.filter((x) => x.groupId === r.id).map((x) => x.userId) }));
}

export async function listGroups(): Promise<GroupView[]> {
  await ensureDb();
  return view(await getDb().select().from(g).orderBy(asc(g.name), asc(g.id)));
}

export async function getGroup(id: string): Promise<GroupView> {
  await ensureDb();
  const [r] = await getDb().select().from(g).where(eq(g.id, id));
  if (!r) throw new ApiError("not_found", "Group not found");
  return (await view([r]))[0]!;
}

async function nameFree(source: string, name: string, except?: string): Promise<void> {
  const [clash] = await getDb().select({ id: g.id }).from(g).where(and(eq(g.source, source), eq(g.name, name)));
  if (clash && clash.id !== except) throw new ApiError("conflict", `A group named ${name} already exists`);
}

export async function createGroup(name: string, source: GroupSource = "local", externalId = ""): Promise<GroupView> {
  await ensureDb();
  const n = cleanName(name);
  await nameFree(source, n);
  const now = new Date();
  const [r] = await getDb().insert(g).values({ id: newId("grp"), name: n, source, externalId, createdAt: now, updatedAt: now }).returning();
  return { id: r!.id, name: r!.name, source: r!.source, member_ids: [] };
}

function localOnly(group: { source: string }): void {
  if (group.source !== "local") throw new ApiError("failed_precondition", `This group comes from ${group.source.toUpperCase()}; change it there`);
}

export async function renameGroup(id: string, name: string, opts: { from?: GroupSource } = {}): Promise<GroupView> {
  const cur = await getGroup(id);
  if (opts.from ? cur.source !== opts.from : cur.source !== "local") localOnly(cur);
  const n = cleanName(name);
  await nameFree(cur.source, n, id);
  await getDb().update(g).set({ name: n, updatedAt: new Date() }).where(eq(g.id, id));
  return { ...cur, name: n };
}

export async function deleteGroup(id: string, opts: { from?: GroupSource } = {}): Promise<void> {
  const cur = await getGroup(id);
  if (opts.from ? cur.source !== opts.from : cur.source !== "local") localOnly(cur);
  await getDb().delete(g).where(eq(g.id, id));
  await clearGroupQuota(id);
}

/** A deleted group's limits go with it. */
async function clearGroupQuota(id: string): Promise<void> {
  const { patchQuotaConfig } = await import("./quotas.ts");
  await patchQuotaConfig({ groups: { [id]: null } });
}

export async function setGroupMembers(id: string, userIds: string[], opts: { from?: GroupSource } = {}): Promise<GroupView> {
  const cur = await getGroup(id);
  if (opts.from ? cur.source !== opts.from : cur.source !== "local") localOnly(cur);
  const ids = [...new Set(userIds)];
  const db = getDb();
  if (ids.length) {
    const found = await db.select({ id: schema.user.id }).from(schema.user).where(inArray(schema.user.id, ids));
    if (found.length !== ids.length) throw new ApiError("not_found", "One of the users does not exist");
  }
  await db.transaction(async (tx) => {
    await tx.delete(m).where(eq(m.groupId, id));
    if (ids.length) await tx.insert(m).values(ids.map((userId) => ({ groupId: id, userId })));
  });
  return { ...cur, member_ids: ids };
}

export async function addGroupMembers(id: string, userIds: string[]): Promise<void> {
  if (!userIds.length) return;
  await getDb().insert(m).values(userIds.map((userId) => ({ groupId: id, userId }))).onConflictDoNothing();
}

export async function removeGroupMembers(id: string, userIds: string[]): Promise<void> {
  if (!userIds.length) return;
  await getDb().delete(m).where(and(eq(m.groupId, id), inArray(m.userId, userIds)));
}

/**
 * Sets a user's membership in the groups of a source to exactly `names`, creating groups that do not exist yet. Called at
 * each sign-in through a directory, so a quota can be set on a directory group and follows its members.
 */
export async function syncSourceGroups(userId: string, source: GroupSource, names: string[]): Promise<void> {
  await ensureDb();
  const db = getDb();
  const wanted = [...new Set(names.map((n) => n.trim()).filter((n) => n && n.length <= 256))];
  const ids: string[] = [];
  for (const name of wanted) {
    const now = new Date();
    const [row] = await db
      .insert(g)
      .values({ id: newId("grp"), name, source, externalId: name, createdAt: now, updatedAt: now })
      .onConflictDoUpdate({ target: [g.source, g.name], set: { updatedAt: now } })
      .returning({ id: g.id });
    ids.push(row!.id);
  }
  const current = await db.select({ id: m.groupId }).from(m).innerJoin(g, eq(g.id, m.groupId)).where(and(eq(m.userId, userId), eq(g.source, source)));
  const remove = current.map((r) => r.id).filter((id) => !ids.includes(id));
  if (remove.length) await db.delete(m).where(and(eq(m.userId, userId), inArray(m.groupId, remove)));
  if (ids.length) await db.insert(m).values(ids.map((groupId) => ({ groupId, userId }))).onConflictDoNothing();
}

/** The groups of one user. */
export async function groupsOfUser(userId: string): Promise<{ id: string; name: string; source: string }[]> {
  await ensureDb();
  return getDb().select({ id: g.id, name: g.name, source: g.source }).from(m).innerJoin(g, eq(g.id, m.groupId)).where(eq(m.userId, userId));
}

/** Group names per user, for lists. */
export async function groupNamesOf(userIds: string[]): Promise<Map<string, string[]>> {
  const out = new Map<string, string[]>();
  if (!userIds.length) return out;
  const rows = await getDb().select({ userId: m.userId, name: g.name }).from(m).innerJoin(g, eq(g.id, m.groupId)).where(inArray(m.userId, userIds)).orderBy(asc(g.name));
  for (const r of rows) out.set(r.userId, [...(out.get(r.userId) ?? []), r.name]);
  return out;
}
