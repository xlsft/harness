/**
 * SCIM 2.0 (RFC 7643, 7644) service provider: Users and Groups, for an identity provider to create, update, deactivate and
 * remove accounts. Accounts are ordinary users without a password; they sign in through SSO, which links them by email.
 *
 * The usual clients differ in details, and those are handled here: PATCH with or without `path`, values as objects or
 * strings, "True" for true (Azure AD), `members[value eq "..."]` paths (Okta, Azure AD). Filtering supports what they use:
 * `eq` on userName, externalId, emails.value, displayName.
 */
import { and, asc, count, eq, inArray } from "drizzle-orm";
import { getAuth } from "../auth/better-auth.ts";
import { activeAdminCount } from "../auth/admins.ts";
import { normalizeRole, roleFromGroups } from "../auth/roles.ts";
import { record } from "../audit/log.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { deleteUser, revokeUserSessions } from "../services/admin-users.ts";
import { addGroupMembers, createGroup, deleteGroup, getGroup, removeGroupMembers, renameGroup, setGroupMembers } from "../services/groups.ts";
import { getScimSettings, scimBaseUrl } from "./tokens.ts";

export const USER_SCHEMA = "urn:ietf:params:scim:schemas:core:2.0:User";
export const GROUP_SCHEMA = "urn:ietf:params:scim:schemas:core:2.0:Group";
export const LIST_SCHEMA = "urn:ietf:params:scim:api:messages:2.0:ListResponse";
export const ERROR_SCHEMA = "urn:ietf:params:scim:api:messages:2.0:Error";
const PATCH_SCHEMA = "urn:ietf:params:scim:api:messages:2.0:PatchOp";

export class ScimError extends Error {
  constructor(readonly status: number, message: string, readonly scimType?: string) {
    super(message);
  }
}

export interface ScimActor {
  id: string;
  name: string;
}

type Json = Record<string, any>;
type UserRow = typeof schema.user.$inferSelect;

const u = schema.user;
const si = schema.scimIdentity;
const g = schema.directoryGroup;
const gm = schema.directoryGroupMember;
const EMAIL = /^[^\s@]+@[^\s@]+$/;
const USERNAME = /^[a-zA-Z0-9_.]{3,30}$/;

const truthy = (v: unknown) => v === true || (typeof v === "string" && v.toLowerCase() === "true");

async function audit(actor: ScimActor, action: string, target: { type: string; id?: string; label?: string }, details: Record<string, unknown> = {}) {
  await record({ action, actor: { id: `scim:${actor.id}`, label: `scim:${actor.name}` }, target, details });
}

function userResource(row: UserRow, identity: { userName: string; externalId: string } | undefined, groups: { id: string; name: string }[]): Json {
  return {
    schemas: [USER_SCHEMA],
    id: row.id,
    ...(identity?.externalId ? { externalId: identity.externalId } : {}),
    userName: identity?.userName ?? row.username ?? row.email,
    displayName: row.name,
    name: { formatted: row.name },
    emails: [{ value: row.email, primary: true, type: "work" }],
    active: !row.banned,
    groups: groups.map((x) => ({ value: x.id, display: x.name, $ref: `${scimBaseUrl()}/Groups/${x.id}` })),
    meta: { resourceType: "User", created: row.createdAt.toISOString(), lastModified: row.updatedAt.toISOString(), location: `${scimBaseUrl()}/Users/${row.id}` },
  };
}

async function loadUser(id: string): Promise<UserRow> {
  await ensureDb();
  const row = await getDb().query.user.findFirst({ where: eq(u.id, id) });
  if (!row) throw new ScimError(404, `User ${id} not found`);
  return row;
}

async function presentUser(row: UserRow): Promise<Json> {
  const db = getDb();
  const identity = await db.query.scimIdentity.findFirst({ where: eq(si.userId, row.id) });
  const groups = await db.select({ id: g.id, name: g.name }).from(gm).innerJoin(g, eq(g.id, gm.groupId)).where(eq(gm.userId, row.id)).orderBy(asc(g.name));
  return userResource(row, identity, groups);
}

/** `attr eq "value"` (the only filter clients need for matching). */
export function parseFilter(filter: string | null): { attr: string; value: string } | undefined {
  if (!filter) return undefined;
  const m = /^\s*([\w.:]+)\s+eq\s+"((?:[^"\\]|\\.)*)"\s*$/i.exec(filter);
  if (!m) throw new ScimError(400, `Unsupported filter: ${filter}`, "invalidFilter");
  return { attr: m[1]!.toLowerCase(), value: m[2]!.replace(/\\(.)/g, "$1") };
}

function pagination(q: URLSearchParams): { offset: number; limit: number; startIndex: number } {
  const startIndex = Math.max(1, Number.parseInt(q.get("startIndex") ?? "1", 10) || 1);
  const n = Number.parseInt(q.get("count") ?? "100", 10);
  return { startIndex, offset: startIndex - 1, limit: Math.min(Number.isFinite(n) && n >= 0 ? n : 100, 500) };
}

export async function listUsers(q: URLSearchParams): Promise<Json> {
  await ensureDb();
  const db = getDb();
  const f = parseFilter(q.get("filter"));
  const { startIndex, offset, limit } = pagination(q);
  let ids: string[] | undefined;
  if (f) {
    const v = f.value;
    if (f.attr === "username") {
      const byIdentity = await db.select({ id: si.userId }).from(si).where(eq(si.userName, v));
      const byUser = byIdentity.length ? byIdentity : await db.select({ id: u.id }).from(u).where(v.includes("@") ? eq(u.email, v.toLowerCase()) : eq(u.username, v.toLowerCase()));
      ids = byUser.map((r) => r.id);
    } else if (f.attr === "externalid") {
      ids = (await db.select({ id: si.userId }).from(si).where(eq(si.externalId, v))).map((r) => r.id);
    } else if (f.attr === "emails.value" || f.attr === "emails") {
      ids = (await db.select({ id: u.id }).from(u).where(eq(u.email, v.toLowerCase()))).map((r) => r.id);
    } else {
      throw new ScimError(400, `Unsupported filter attribute: ${f.attr}`, "invalidFilter");
    }
  }
  const where = ids ? inArray(u.id, ids.length ? ids : ["-"]) : undefined;
  const [total] = await db.select({ n: count() }).from(u).where(where);
  const rows = await db.select().from(u).where(where).orderBy(asc(u.createdAt), asc(u.id)).offset(offset).limit(limit);
  return { schemas: [LIST_SCHEMA], totalResults: total?.n ?? 0, startIndex, itemsPerPage: rows.length, Resources: await Promise.all(rows.map(presentUser)) };
}

export async function getUser(id: string): Promise<Json> {
  return presentUser(await loadUser(id));
}

interface UserFields {
  userName?: string;
  externalId?: string;
  name?: string;
  email?: string;
  active?: boolean;
}

function fieldsFromResource(b: Json): UserFields {
  const emails = Array.isArray(b.emails) ? b.emails : [];
  const primary = emails.find((e: Json) => truthy(e?.primary)) ?? emails[0];
  const n = b.name ?? {};
  const name = b.displayName || n.formatted || [n.givenName, n.familyName].filter(Boolean).join(" ");
  return {
    userName: typeof b.userName === "string" ? b.userName.trim() : undefined,
    externalId: typeof b.externalId === "string" ? b.externalId : undefined,
    name: typeof name === "string" && name ? name : undefined,
    email: typeof primary?.value === "string" ? primary.value.trim().toLowerCase() : undefined,
    active: b.active === undefined ? undefined : truthy(b.active),
  };
}

function emailOf(f: UserFields): string {
  const email = f.email ?? (f.userName?.includes("@") ? f.userName.toLowerCase() : "");
  if (!email || !EMAIL.test(email)) throw new ScimError(400, "A user needs an email address (emails, or an address as userName)", "invalidValue");
  return email;
}

export async function createUser(body: Json, actor: ScimActor): Promise<Json> {
  await ensureDb();
  const db = getDb();
  const f = fieldsFromResource(body);
  if (!f.userName) throw new ScimError(400, "userName is required", "invalidValue");
  const email = emailOf(f);
  if (await db.query.scimIdentity.findFirst({ where: eq(si.userName, f.userName) })) throw new ScimError(409, "A user with this userName already exists", "uniqueness");
  const now = new Date();
  // An account an admin or a sign-in made before the provisioning client got to it is adopted, not duplicated.
  let row = await db.query.user.findFirst({ where: eq(u.email, email) });
  if (row && (await db.query.scimIdentity.findFirst({ where: eq(si.userId, row.id) }))) throw new ScimError(409, "A user with this email already exists", "uniqueness");
  if (!row) {
    const handle = USERNAME.test(f.userName) && !(await db.query.user.findFirst({ where: eq(u.username, f.userName.toLowerCase()) })) ? f.userName.toLowerCase() : undefined;
    const ctx = await getAuth().$context;
    const created = await ctx.internalAdapter.createUser({ email, name: f.name ?? f.userName, emailVerified: true, ...(handle ? { username: handle, displayUsername: f.userName } : {}) }, { method: "scim" });
    row = (await db.query.user.findFirst({ where: eq(u.id, created.id) }))!;
  }
  await db.insert(si).values({ userId: row.id, userName: f.userName, externalId: f.externalId ?? "", createdAt: now, updatedAt: now });
  if (f.active === false) await deactivate(row);
  await audit(actor, "scim.user_create", { type: "user", id: row.id, label: email });
  return presentUser(await loadUser(row.id));
}

async function deactivate(row: UserRow): Promise<void> {
  if (row.role === "admin" && !row.banned && (await activeAdminCount(row.id)) === 0) throw new ScimError(409, "This is the last admin; it cannot be deactivated", "mutability");
  const ctx = await getAuth().$context;
  await ctx.internalAdapter.updateUser(row.id, { banned: true, banReason: "Deactivated by the identity provider", banExpires: null, updatedAt: new Date() });
  await revokeUserSessions(row.id);
}

async function applyUser(row: UserRow, f: UserFields, actor: ScimActor): Promise<UserRow> {
  const db = getDb();
  const set: Partial<UserRow> = {};
  if (f.name && f.name !== row.name) set.name = f.name;
  if (f.email && f.email !== row.email) {
    if (!EMAIL.test(f.email)) throw new ScimError(400, "Invalid email address", "invalidValue");
    if (await db.query.user.findFirst({ where: eq(u.email, f.email) })) throw new ScimError(409, "A user with this email already exists", "uniqueness");
    set.email = f.email;
  }
  if (Object.keys(set).length) await db.update(u).set({ ...set, updatedAt: new Date() }).where(eq(u.id, row.id));
  if (f.userName !== undefined || f.externalId !== undefined) {
    const now = new Date();
    const userName = f.userName || (await db.query.scimIdentity.findFirst({ where: eq(si.userId, row.id) }))?.userName || row.username || row.email;
    const clash = await db.query.scimIdentity.findFirst({ where: eq(si.userName, userName) });
    if (clash && clash.userId !== row.id) throw new ScimError(409, "A user with this userName already exists", "uniqueness");
    const values: { userName: string; updatedAt: Date; externalId?: string } = { userName, updatedAt: now };
    if (f.externalId !== undefined) values.externalId = f.externalId;
    await db.insert(si).values({ userId: row.id, externalId: "", createdAt: now, ...values }).onConflictDoUpdate({ target: si.userId, set: values });
  }
  if (f.active !== undefined && f.active === !!row.banned) {
    if (f.active) {
      const ctx = await getAuth().$context;
      await ctx.internalAdapter.updateUser(row.id, { banned: false, banReason: null, banExpires: null, updatedAt: new Date() });
    } else {
      await deactivate(row);
    }
    await audit(actor, f.active ? "scim.user_activate" : "scim.user_deactivate", { type: "user", id: row.id, label: row.email });
  }
  return loadUser(row.id);
}

export async function replaceUser(id: string, body: Json, actor: ScimActor): Promise<Json> {
  const row = await loadUser(id);
  const updated = await applyUser(row, fieldsFromResource(body), actor);
  await audit(actor, "scim.user_update", { type: "user", id, label: updated.email });
  return presentUser(updated);
}

/** The field a PATCH path names, in the user's terms. */
function patchUserFields(ops: Json[]): UserFields {
  const f: UserFields = {};
  const assign = (path: string, value: unknown) => {
    const p = path.toLowerCase();
    if (p === "active") f.active = truthy(value);
    else if (p === "username") f.userName = String(value);
    else if (p === "externalid") f.externalId = String(value);
    else if (p === "displayname" || p === "name.formatted") f.name = String(value);
    else if (p.startsWith("emails")) f.email = String(Array.isArray(value) ? (value[0] as Json)?.value : ((value as Json)?.value ?? value)).trim().toLowerCase();
    // Other attributes (title, locale, ...) have no place here. Accepting them keeps clients from failing the whole run.
  };
  for (const op of ops) {
    const kind = String(op.op ?? "").toLowerCase();
    if (kind !== "add" && kind !== "replace") throw new ScimError(400, `Unsupported operation on a user: ${op.op}`, "invalidSyntax");
    if (op.path) assign(String(op.path), op.value);
    else if (op.value && typeof op.value === "object") for (const [k, v] of Object.entries(op.value)) assign(k, v);
  }
  return f;
}

export function patchOps(body: Json): Json[] {
  if (Array.isArray(body.schemas) && !body.schemas.includes(PATCH_SCHEMA)) throw new ScimError(400, "Not a PatchOp", "invalidSyntax");
  if (!Array.isArray(body.Operations)) throw new ScimError(400, "Operations are required", "invalidSyntax");
  return body.Operations as Json[];
}

export async function patchUser(id: string, body: Json, actor: ScimActor): Promise<Json> {
  const row = await loadUser(id);
  const updated = await applyUser(row, patchUserFields(patchOps(body)), actor);
  await audit(actor, "scim.user_update", { type: "user", id, label: updated.email });
  return presentUser(updated);
}

export async function removeUser(id: string, actor: ScimActor): Promise<void> {
  const row = await loadUser(id);
  if (row.role === "admin" && !row.banned && (await activeAdminCount(row.id)) === 0) throw new ScimError(409, "This is the last admin; it cannot be deleted", "mutability");
  await deleteUser(id, "");
  await audit(actor, "scim.user_delete", { type: "user", id, label: row.email });
}

// --- groups -------------------------------------------------------------------------------------------------

async function presentGroup(row: typeof g.$inferSelect): Promise<Json> {
  const members = await getDb().select({ value: gm.userId, display: u.email }).from(gm).innerJoin(u, eq(u.id, gm.userId)).where(eq(gm.groupId, row.id));
  return {
    schemas: [GROUP_SCHEMA],
    id: row.id,
    ...(row.externalId ? { externalId: row.externalId } : {}),
    displayName: row.name,
    members: members.map((m) => ({ value: m.value, display: m.display, type: "User", $ref: `${scimBaseUrl()}/Users/${m.value}` })),
    meta: { resourceType: "Group", created: row.createdAt.toISOString(), lastModified: row.updatedAt.toISOString(), location: `${scimBaseUrl()}/Groups/${row.id}` },
  };
}

async function scimGroup(id: string): Promise<typeof g.$inferSelect> {
  await ensureDb();
  const [row] = await getDb().select().from(g).where(and(eq(g.id, id), eq(g.source, "scim")));
  if (!row) throw new ScimError(404, `Group ${id} not found`);
  return row;
}

/** Applies the role mapping to the members whose SCIM groups changed. The last active admin is never demoted. */
async function remapRoles(userIds: string[]): Promise<void> {
  const { role_mappings: rules } = await getScimSettings();
  if (!rules.length || !userIds.length) return;
  const db = getDb();
  for (const id of new Set(userIds)) {
    const names = (await db.select({ name: g.name }).from(gm).innerJoin(g, eq(g.id, gm.groupId)).where(and(eq(gm.userId, id), eq(g.source, "scim")))).map((r) => r.name);
    const role = roleFromGroups(rules, names);
    const row = await db.query.user.findFirst({ where: eq(u.id, id) });
    if (!row || normalizeRole(row.role) === role) continue;
    if (row.role === "admin" && !row.banned && (await activeAdminCount(id)) === 0) continue;
    await db.update(u).set({ role, updatedAt: new Date() }).where(eq(u.id, id));
  }
}

async function memberIds(groupId: string): Promise<string[]> {
  return (await getDb().select({ id: gm.userId }).from(gm).where(eq(gm.groupId, groupId))).map((r) => r.id);
}

export async function listGroups(q: URLSearchParams): Promise<Json> {
  await ensureDb();
  const db = getDb();
  const f = parseFilter(q.get("filter"));
  const { startIndex, offset, limit } = pagination(q);
  const conds = [eq(g.source, "scim")];
  if (f) {
    if (f.attr === "displayname") conds.push(eq(g.name, f.value));
    else if (f.attr === "externalid") conds.push(eq(g.externalId, f.value));
    else throw new ScimError(400, `Unsupported filter attribute: ${f.attr}`, "invalidFilter");
  }
  const where = and(...conds);
  const [total] = await db.select({ n: count() }).from(g).where(where);
  const rows = await db.select().from(g).where(where).orderBy(asc(g.name)).offset(offset).limit(limit);
  return { schemas: [LIST_SCHEMA], totalResults: total?.n ?? 0, startIndex, itemsPerPage: rows.length, Resources: await Promise.all(rows.map(presentGroup)) };
}

export async function getScimGroup(id: string): Promise<Json> {
  return presentGroup(await scimGroup(id));
}

function membersOf(body: Json): string[] {
  return (Array.isArray(body.members) ? body.members : []).map((m: Json) => String(m?.value ?? "")).filter(Boolean);
}

async function existing(ids: string[]): Promise<string[]> {
  if (!ids.length) return [];
  return (await getDb().select({ id: u.id }).from(u).where(inArray(u.id, ids))).map((r) => r.id);
}

export async function createScimGroup(body: Json, actor: ScimActor): Promise<Json> {
  await ensureDb();
  const name = typeof body.displayName === "string" ? body.displayName.trim() : "";
  if (!name) throw new ScimError(400, "displayName is required", "invalidValue");
  const [clash] = await getDb().select({ id: g.id }).from(g).where(and(eq(g.source, "scim"), eq(g.name, name)));
  if (clash) throw new ScimError(409, "A group with this name already exists", "uniqueness");
  const created = await createGroup(name, "scim", typeof body.externalId === "string" ? body.externalId : "");
  const ids = await existing(membersOf(body));
  await addGroupMembers(created.id, ids);
  await remapRoles(ids);
  await audit(actor, "scim.group_create", { type: "group", id: created.id, label: name }, { members: ids.length });
  return getScimGroup(created.id);
}

export async function replaceScimGroup(id: string, body: Json, actor: ScimActor): Promise<Json> {
  const row = await scimGroup(id);
  const before = await memberIds(id);
  if (typeof body.displayName === "string" && body.displayName.trim() !== row.name) await renameGroup(id, body.displayName, { from: "scim" });
  if (typeof body.externalId === "string") await getDb().update(g).set({ externalId: body.externalId }).where(eq(g.id, id));
  const ids = await existing(membersOf(body));
  await setGroupMembers(id, ids, { from: "scim" });
  await remapRoles([...before, ...ids]);
  await audit(actor, "scim.group_update", { type: "group", id, label: row.name });
  return getScimGroup(id);
}

export async function patchScimGroup(id: string, body: Json, actor: ScimActor): Promise<Json> {
  const row = await scimGroup(id);
  const touched: string[] = [];
  for (const op of patchOps(body)) {
    const kind = String(op.op ?? "").toLowerCase();
    const path = String(op.path ?? "");
    const filtered = /^members\[value eq "([^"]+)"\]$/i.exec(path);
    if (kind === "remove" && filtered) {
      await removeGroupMembers(id, [filtered[1]!]);
      touched.push(filtered[1]!);
    } else if (path.toLowerCase() === "members" || (!path && Array.isArray(op.value?.members))) {
      const values = Array.isArray(op.value) ? op.value : (op.value?.members ?? []);
      const ids = (values as Json[]).map((m) => String(m?.value ?? "")).filter(Boolean);
      if (kind === "add") await addGroupMembers(id, await existing(ids));
      else if (kind === "remove") await removeGroupMembers(id, ids.length ? ids : await memberIds(id));
      else if (kind === "replace") {
        touched.push(...(await memberIds(id)));
        await setGroupMembers(id, await existing(ids), { from: "scim" });
      } else throw new ScimError(400, `Unsupported operation: ${op.op}`, "invalidSyntax");
      touched.push(...ids);
    } else if ((kind === "replace" || kind === "add") && (path.toLowerCase() === "displayname" || (!path && op.value?.displayName))) {
      await renameGroup(id, String(path ? op.value : op.value.displayName), { from: "scim" });
    } else if ((kind === "replace" || kind === "add") && path.toLowerCase() === "externalid") {
      await getDb().update(g).set({ externalId: String(op.value) }).where(eq(g.id, id));
    } else {
      throw new ScimError(400, `Unsupported operation: ${op.op} ${path}`, "invalidPath");
    }
  }
  await remapRoles(touched);
  await audit(actor, "scim.group_update", { type: "group", id, label: row.name }, { members_touched: touched.length });
  return getScimGroup(id);
}

export async function removeScimGroup(id: string, actor: ScimActor): Promise<void> {
  const row = await scimGroup(id);
  const before = (await getGroup(id)).member_ids;
  await deleteGroup(id, { from: "scim" });
  await remapRoles(before);
  await audit(actor, "scim.group_delete", { type: "group", id, label: row.name });
}

// --- discovery ------------------------------------------------------------------------------------------------

export function serviceProviderConfig(): Json {
  return {
    schemas: ["urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"],
    documentationUri: "https://github.com/xlsft/harness/tree/main/deploy#scim",
    patch: { supported: true },
    bulk: { supported: false, maxOperations: 0, maxPayloadSize: 0 },
    filter: { supported: true, maxResults: 500 },
    changePassword: { supported: false },
    sort: { supported: false },
    etag: { supported: false },
    authenticationSchemes: [{ type: "oauthbearertoken", name: "Bearer token", description: "A token from Settings, Provisioning", primary: true }],
    meta: { resourceType: "ServiceProviderConfig", location: `${scimBaseUrl()}/ServiceProviderConfig` },
  };
}

export function resourceTypes(): Json {
  const types = [
    { id: "User", name: "User", endpoint: "/Users", schema: USER_SCHEMA },
    { id: "Group", name: "Group", endpoint: "/Groups", schema: GROUP_SCHEMA },
  ].map((t) => ({ schemas: ["urn:ietf:params:scim:schemas:core:2.0:ResourceType"], ...t, meta: { resourceType: "ResourceType", location: `${scimBaseUrl()}/ResourceTypes/${t.id}` } }));
  return { schemas: [LIST_SCHEMA], totalResults: types.length, startIndex: 1, itemsPerPage: types.length, Resources: types };
}

const attr = (name: string, type: string, extra: Json = {}) => ({ name, type, multiValued: false, required: false, caseExact: false, mutability: "readWrite", returned: "default", uniqueness: "none", ...extra });

export function schemas(): Json {
  const list = [
    {
      id: USER_SCHEMA,
      name: "User",
      attributes: [
        attr("userName", "string", { required: true, uniqueness: "server" }),
        attr("displayName", "string"),
        attr("externalId", "string", { caseExact: true }),
        attr("active", "boolean"),
        attr("name", "complex", { subAttributes: [attr("formatted", "string"), attr("givenName", "string"), attr("familyName", "string")] }),
        attr("emails", "complex", { multiValued: true, subAttributes: [attr("value", "string"), attr("primary", "boolean"), attr("type", "string")] }),
      ],
    },
    {
      id: GROUP_SCHEMA,
      name: "Group",
      attributes: [attr("displayName", "string", { required: true }), attr("externalId", "string", { caseExact: true }), attr("members", "complex", { multiValued: true, subAttributes: [attr("value", "string"), attr("display", "string", { mutability: "readOnly" })] })],
    },
  ].map((s) => ({ schemas: ["urn:ietf:params:scim:schemas:core:2.0:Schema"], ...s, meta: { resourceType: "Schema", location: `${scimBaseUrl()}/Schemas/${s.id}` } }));
  return { schemas: [LIST_SCHEMA], totalResults: list.length, startIndex: 1, itemsPerPage: list.length, Resources: list };
}
