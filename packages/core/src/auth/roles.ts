/**
 * Roles and what they allow (docs/design/roles.md). A role is a named set of permissions from the fixed catalog below;
 * nothing outside the catalog can be granted, and every administrative entry point names the permission it needs and
 * calls need() or can(). Everyone uses the product itself the same way (models, sessions): roles only decide who
 * administers the server.
 *
 * Built-in roles have fixed ids and fixed permission sets: `admin` holds every permission; `auditor` reads what the
 * server is administered with and changes nothing; `billing` reads usage and sets spend quotas; `user` has no
 * administration. Custom roles ("role_…" ids) live in the custom_role table (services/roles.ts) and are mirrored into
 * this module, so the checks stay synchronous. An id this module does not know is the `user` role: deny by default.
 */
import { ApiError } from "../lib/errors.ts";

export const BUILTIN_ROLES = ["user", "admin", "auditor", "billing"] as const;
export type BuiltinRole = (typeof BUILTIN_ROLES)[number];
/** Kept for older imports: the built-in role ids. */
export const ROLES = BUILTIN_ROLES;
/** A role id: a built-in or a custom role's "role_…". */
export type Role = string;

export interface PermissionInfo {
  id: string;
  area: string;
  area_label: string;
  label: string;
  description: string;
}

const AREAS: Record<string, string> = {
  users: "Users",
  roles: "Roles",
  directory: "Groups and provisioning",
  quotas: "Usage and quotas",
  audit: "Audit",
  policy: "Policy",
  sign_in: "Sign-in",
  providers: "Providers and models",
  limits: "Provider limits",
  mcp: "MCP servers",
  hosts: "Always-on hosts",
  projects: "Projects",
  connections: "Code host connections",
};

const P = (id: string, area: string, label: string, description: string): PermissionInfo => ({ id, area, area_label: AREAS[area]!, label, description });

/** The catalog. Order is display order; ids never change meaning once released. */
export const PERMISSION_CATALOG: readonly PermissionInfo[] = [
  P("users.read", "users", "See users", "List accounts with their roles, groups, sign-in methods and devices"),
  P("users.write", "users", "Manage users", "Create, disable and delete accounts, reset passwords, give roles, revoke devices"),
  P("roles.read", "roles", "See roles", "List roles and the permissions they grant"),
  P("roles.write", "roles", "Manage roles", "Create, change and delete custom roles"),
  P("directory.read", "directory", "See groups", "List groups and their members"),
  P("directory.write", "directory", "Manage groups", "Create, rename and delete local groups and change their members"),
  P("usage.read", "quotas", "See usage", "Usage and cost of everyone, as tables and CSV"),
  P("quotas.read", "quotas", "See spend limits", "Spend limits per user, group and role, and the session budget"),
  P("quotas.write", "quotas", "Set spend limits", "Change spend limits per user, group and role, and the session budget"),
  P("audit.read", "audit", "Read the audit log", "The audit log and the decision log, with CSV export"),
  P("policy.read", "policy", "See policy", "The managed policy, permission modes and local model policy"),
  P("policy.write", "policy", "Change policy", "Change the managed policy, permission modes and local model policy"),
  P("policy.documents", "policy", "Write organization documents", "Write and delete organization settings, policies and skills distributed to clients"),
  P("sign_in.read", "sign_in", "See sign-in settings", "LDAP, SSO, SCIM, two-factor policy and role mappings"),
  P("sign_in.write", "sign_in", "Change sign-in settings", "Change LDAP, SSO, SCIM tokens, two-factor policy and role mappings"),
  P("providers.write", "providers", "Manage providers", "Global LLM, speech, search and decision providers, models, default models, and every provider's balance"),
  P("limits.read", "limits", "See provider limit policies", "Who sees each global provider's balance and limits"),
  P("limits.write", "limits", "Set provider limit policies", "Change who sees each global provider's balance and limits"),
  P("mcp.write", "mcp", "Manage organization MCP servers", "Add, change and remove organization MCP servers, see every one, allow private networks"),
  P("mcp.audit", "mcp", "Read the MCP audit log", "Every MCP call made through the server's proxy"),
  P("hosts.read", "hosts", "See every host", "Every always-on host and the work on it"),
  P("hosts.write", "hosts", "Manage hosts", "Create team hosts, change and remove any host"),
  P("projects.write", "projects", "Manage global projects", "Create global projects and manage every global project"),
  P("connections.write", "connections", "Manage code host connections", "Connect GitHub and GitLab, see every repository event"),
];

export const CAPABILITIES: readonly string[] = PERMISSION_CATALOG.map((p) => p.id);
/** A permission id from the catalog. */
export type Capability =
  | "users.read"
  | "users.write"
  | "roles.read"
  | "roles.write"
  | "directory.read"
  | "directory.write"
  | "usage.read"
  | "quotas.read"
  | "quotas.write"
  | "audit.read"
  | "policy.read"
  | "policy.write"
  | "policy.documents"
  | "sign_in.read"
  | "sign_in.write"
  | "providers.write"
  | "limits.read"
  | "limits.write"
  | "mcp.write"
  | "mcp.audit"
  | "hosts.read"
  | "hosts.write"
  | "projects.write"
  | "connections.write";

const ALL = CAPABILITIES as readonly Capability[];

/**
 * The built-ins. auditor is the reads it had before custom roles plus roles.read; permissions added later for reads that
 * were admin-only (mcp.audit) stay admin-only, so no built-in gains access by a new permission.
 */
const BUILTIN_TABLE: Record<BuiltinRole, readonly Capability[]> = {
  admin: ALL,
  auditor: ["users.read", "roles.read", "directory.read", "usage.read", "quotas.read", "audit.read", "policy.read", "sign_in.read", "limits.read", "hosts.read"],
  // Who the usage is of, and the group names quotas are set on, come with the job; nothing about their accounts' sign-in.
  billing: ["usage.read", "quotas.read", "quotas.write", "users.read", "directory.read", "limits.read"],
  user: [],
};

export const BUILTIN_NAMES: Record<BuiltinRole, { name: string; description: string }> = {
  admin: { name: "Admin", description: "Every permission. The last active admin cannot lose this role." },
  auditor: { name: "Auditor", description: "Reads what the server is administered with and changes nothing." },
  billing: { name: "Billing", description: "Reads usage and sets spend quotas." },
  user: { name: "User", description: "Uses the product; no administration." },
};

export interface CustomRole {
  id: string;
  name: string;
  description: string;
  permissions: Capability[];
}

let custom = new Map<string, CustomRole>();

export function isBuiltin(id: string): id is BuiltinRole {
  return (BUILTIN_ROLES as readonly string[]).includes(id);
}

export function isPermission(p: string): p is Capability {
  return CAPABILITIES.includes(p);
}

/** Replaces the mirror of the custom_role table. Permissions outside the catalog are dropped. */
export function setCustomRoles(list: { id: string; name: string; description: string; permissions: unknown }[]): void {
  const next = new Map<string, CustomRole>();
  for (const r of list) {
    if (isBuiltin(r.id)) continue;
    const perms = Array.isArray(r.permissions) ? (r.permissions as unknown[]).filter((p): p is Capability => typeof p === "string" && isPermission(p)) : [];
    next.set(r.id, { id: r.id, name: r.name, description: r.description, permissions: [...new Set(perms)].sort() });
  }
  custom = next;
}

export function customRoles(): CustomRole[] {
  return [...custom.values()];
}

/** Whether `id` names a role that exists (built-in or a known custom one). */
export function roleExists(id: string): boolean {
  return isBuiltin(id) || custom.has(id);
}

/** A stored role value as a role id; unknown (or a deleted custom role) is "user". */
export function normalizeRole(r: string | null | undefined): Role {
  const v = r ?? "";
  return roleExists(v) ? v : "user";
}

export function roleName(id: string): string {
  if (isBuiltin(id)) return BUILTIN_NAMES[id].name;
  return custom.get(id)?.name ?? BUILTIN_NAMES.user.name;
}

export function capabilitiesOf(role: Role): readonly Capability[] {
  const id = normalizeRole(role);
  if (isBuiltin(id)) return BUILTIN_TABLE[id];
  return custom.get(id)?.permissions ?? [];
}

export function can(user: { role: string }, cap: Capability): boolean {
  return capabilitiesOf(user.role).includes(cap);
}

export function need(user: { role: string }, cap: Capability): void {
  if (!can(user, cap)) throw new ApiError("permission", `Your role does not allow this (${cap})`);
}

/** Whether `user` has any of `caps`. */
export function canAny(user: { role: string }, caps: Capability[]): boolean {
  return caps.some((c) => can(user, c));
}

/** Whether every permission of `perms` is the caller's own. */
export function holdsAll(user: { role: string }, perms: readonly string[]): boolean {
  const mine = capabilitiesOf(user.role);
  return perms.every((p) => mine.includes(p as Capability));
}

/**
 * Refuses giving (or taking away) `roleId` unless the caller holds every permission of it: nobody hands out, or
 * reaches into, more than they have themselves.
 */
export function assertMayGrant(caller: { role: string }, roleId: string): void {
  if (!holdsAll(caller, capabilitiesOf(roleId))) {
    throw new ApiError("permission", `The role ${roleName(roleId)} has permissions your role does not have`);
  }
}

/** A role id from the outside, or invalid_request. */
export function parseRole(v: string): Role {
  if (!roleExists(v)) throw new ApiError("invalid_request", `Unknown role ${JSON.stringify(v)}`);
  return v;
}

export interface RoleMapping {
  group: string;
  role: Role;
}

/**
 * Cleans group to role rules. Role ids are kept as stored: callers taking rules from the outside check them first
 * (rpc/roles.ts, roleFromProto), and a rule naming a role that no longer exists gives "user" where it is applied
 * (roleFromGroups), so a deleted custom role never breaks sign-in.
 */
export function cleanMappings(input: { group?: unknown; role?: unknown }[] | undefined): RoleMapping[] {
  const out: RoleMapping[] = [];
  for (const m of input ?? []) {
    const group = typeof m.group === "string" ? m.group.trim() : "";
    if (!group) continue;
    const role = typeof m.role === "string" ? m.role.trim() : "";
    out.push({ group, role: role || "user" });
  }
  return out;
}

/**
 * The first rule a member of `groups` matches, by `same` (group names differ in comparison rules by source). A rule naming
 * an unknown role gives "user"; callers make sure the roles are fresh (services/roles.ts rolesFresh) before applying it.
 */
export function roleFromGroups(rules: RoleMapping[], groups: string[], same: (a: string, b: string) => boolean = (a, b) => a === b): Role {
  for (const r of rules) if (groups.some((g) => same(g, r.group))) return normalizeRole(r.role);
  return "user";
}
