/**
 * Custom roles: the custom_role table, and the mirror of it in auth/roles.ts the permission checks read. Every process
 * reloads the mirror when it is older than RELOAD_MS (rolesFresh(), called where a caller is authenticated and where
 * sign-in maps directory groups to roles) and at once after a change it made itself, so another replica sees a change
 * within seconds; until then it errs on the side of its older copy, and a role it does not know is "user".
 *
 * Rules: names are unique ignoring case, built-in names included; permissions come from the catalog only; nobody
 * creates, changes or deletes a role with a permission they do not hold; built-ins are neither changed nor deleted;
 * deleting a role somebody has needs the caller's confirmation, and moves them to "user" first.
 */
import { asc, count, eq } from "drizzle-orm";
import {
  BUILTIN_NAMES,
  BUILTIN_ROLES,
  capabilitiesOf,
  customRoles,
  holdsAll,
  isBuiltin,
  isPermission,
  setCustomRoles,
  type Capability,
} from "../auth/roles.ts";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";

const RELOAD_MS = 5_000;
let loadedAt = 0;
let loading: Promise<void> | undefined;

const r = schema.customRole;

/** Reloads the mirror of the custom_role table. */
export async function reloadRoles(): Promise<void> {
  await ensureDb();
  const rows = await getDb().select().from(r);
  setCustomRoles(rows.map((x) => ({ id: x.id, name: x.name, description: x.description, permissions: x.permissions })));
  loadedAt = Date.now();
}

/** Makes sure the mirror is at most RELOAD_MS old. */
export function rolesFresh(): Promise<void> {
  if (Date.now() - loadedAt < RELOAD_MS) return Promise.resolve();
  loading ??= reloadRoles().finally(() => {
    loading = undefined;
  });
  return loading;
}

export interface RoleView {
  id: string;
  name: string;
  description: string;
  builtin: boolean;
  editable: boolean;
  permissions: string[];
  member_count: number;
  created_at: string;
  updated_at: string;
}

async function memberCounts(): Promise<Map<string, number>> {
  const u = schema.user;
  const rows = await getDb().select({ role: u.role, n: count() }).from(u).groupBy(u.role);
  const out = new Map<string, number>();
  for (const x of rows) {
    const id = x.role && (isBuiltin(x.role) || customRoles().some((c) => c.id === x.role)) ? x.role : "user";
    out.set(id, (out.get(id) ?? 0) + x.n);
  }
  return out;
}

/** Built-ins first (admin, auditor, billing, user), then custom roles by name. */
export async function listRoles(caller: { role: string }): Promise<RoleView[]> {
  await reloadRoles();
  const counts = await memberCounts();
  const rows = await getDb().select().from(r).orderBy(asc(r.nameKey));
  const builtins = (["admin", "auditor", "billing", "user"] as const).map((id) => ({
    id,
    name: BUILTIN_NAMES[id].name,
    description: BUILTIN_NAMES[id].description,
    builtin: true,
    editable: false,
    permissions: [...capabilitiesOf(id)],
    member_count: counts.get(id) ?? 0,
    created_at: "",
    updated_at: "",
  }));
  return [...builtins, ...rows.map((x) => rowView(x, counts.get(x.id) ?? 0, caller))];
}

function rowView(x: typeof r.$inferSelect, members: number, caller: { role: string }): RoleView {
  const permissions = cleanPermissions(x.permissions as unknown[], false);
  return {
    id: x.id,
    name: x.name,
    description: x.description,
    builtin: false,
    editable: holdsAll(caller, permissions),
    permissions,
    member_count: members,
    created_at: x.createdAt.toISOString(),
    updated_at: x.updatedAt.toISOString(),
  };
}

/** Catalog permissions only, sorted, without duplicates. strict: an unknown one is invalid_request. */
function cleanPermissions(input: unknown[], strict: boolean): Capability[] {
  const out = new Set<Capability>();
  for (const p of input ?? []) {
    if (typeof p === "string" && isPermission(p)) out.add(p);
    else if (strict) throw new ApiError("invalid_request", `Unknown permission ${JSON.stringify(p)}`);
  }
  return [...out].sort();
}

function cleanName(name: string): string {
  const n = (name ?? "").trim();
  if (!n || n.length > 64) throw new ApiError("invalid_request", "A role name has 1 to 64 characters");
  return n;
}

function cleanDescription(d: string): string {
  const v = (d ?? "").trim();
  if (v.length > 500) throw new ApiError("invalid_request", "A role description has at most 500 characters");
  return v;
}

async function nameFree(name: string, except?: string): Promise<void> {
  const key = name.toLowerCase();
  if (BUILTIN_ROLES.some((b) => b === key || BUILTIN_NAMES[b].name.toLowerCase() === key)) throw new ApiError("conflict", `${name} is a built-in role`);
  const [clash] = await getDb().select({ id: r.id }).from(r).where(eq(r.nameKey, key));
  if (clash && clash.id !== except) throw new ApiError("conflict", `A role named ${name} already exists`);
}

function mayHold(caller: { role: string }, perms: readonly string[]): void {
  if (!holdsAll(caller, perms)) throw new ApiError("permission", "A role cannot have permissions your own role does not have");
}

async function getRow(id: string): Promise<typeof r.$inferSelect> {
  if (isBuiltin(id)) throw new ApiError("failed_precondition", "Built-in roles cannot be changed");
  const [x] = await getDb().select().from(r).where(eq(r.id, id));
  if (!x) throw new ApiError("not_found", "Role not found");
  return x;
}

export async function createRole(caller: { role: string }, input: { name: string; description: string; permissions: string[] }): Promise<RoleView> {
  await ensureDb();
  const name = cleanName(input.name);
  const description = cleanDescription(input.description);
  const permissions = cleanPermissions(input.permissions, true);
  mayHold(caller, permissions);
  await nameFree(name);
  const now = new Date();
  const [x] = await getDb()
    .insert(r)
    .values({ id: newId("role"), name, nameKey: name.toLowerCase(), description, permissions, createdAt: now, updatedAt: now })
    .returning();
  await reloadRoles();
  return rowView(x!, 0, caller);
}

export async function updateRole(
  caller: { role: string },
  id: string,
  input: { name?: string; description?: string; permissions?: string[] },
): Promise<RoleView> {
  await ensureDb();
  const cur = await getRow(id);
  // Changing a role reaches everybody who has it: the caller must hold what it grants now and what it will grant.
  mayHold(caller, cleanPermissions(cur.permissions as unknown[], false));
  const set: Partial<typeof r.$inferInsert> = { updatedAt: new Date() };
  if (input.name !== undefined) {
    const name = cleanName(input.name);
    await nameFree(name, id);
    set.name = name;
    set.nameKey = name.toLowerCase();
  }
  if (input.description !== undefined) set.description = cleanDescription(input.description);
  if (input.permissions !== undefined) {
    const permissions = cleanPermissions(input.permissions, true);
    mayHold(caller, permissions);
    set.permissions = permissions;
  }
  const [x] = await getDb().update(r).set(set).where(eq(r.id, id)).returning();
  await reloadRoles();
  return rowView(x!, (await memberCounts()).get(id) ?? 0, caller);
}

/** Deletes a custom role; with members, only when moveMembers confirms they become users. */
export async function deleteRole(caller: { role: string }, id: string, moveMembers: boolean): Promise<{ moved: number }> {
  await ensureDb();
  const cur = await getRow(id);
  mayHold(caller, cleanPermissions(cur.permissions as unknown[], false));
  const u = schema.user;
  const db = getDb();
  const [{ n } = { n: 0 }] = await db.select({ n: count() }).from(u).where(eq(u.role, id));
  if (n > 0 && !moveMembers) {
    throw new ApiError("failed_precondition", `${n} user(s) have the role ${cur.name}; confirm to move them to User`);
  }
  await db.transaction(async (tx) => {
    if (n > 0) await tx.update(u).set({ role: "user", updatedAt: new Date() }).where(eq(u.role, id));
    await tx.delete(r).where(eq(r.id, id));
  });
  await reloadRoles();
  // Its limits go with it. Rules and shares that name it stay, and reach nobody: a role id is never given out again.
  const { patchQuotaConfig } = await import("./quotas.ts");
  await patchQuotaConfig({ roles: { [id]: null } });
  return { moved: n };
}
