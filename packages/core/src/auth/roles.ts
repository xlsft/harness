/**
 * Roles and what they allow. `admin` may do everything; `auditor` reads everything an admin can read about the server and
 * changes nothing; `billing` reads usage and sets spend quotas; `user` has no administration. Everyone uses the product
 * itself the same way (models, sessions): roles only decide who administers the server.
 *
 * Every administrative entry point names the capability it needs and calls need() or can(), so what a role may do is
 * this one table.
 */
import { ApiError } from "../lib/errors.ts";

export const ROLES = ["user", "admin", "auditor", "billing"] as const;
export type Role = (typeof ROLES)[number];

export const CAPABILITIES = [
  "users.read",
  "users.write",
  "audit.read",
  "usage.read",
  "quotas.read",
  "quotas.write",
  "policy.read",
  "policy.write",
  "sign_in.read",
  "sign_in.write",
  "directory.read",
  "directory.write",
  "limits.read",
  "limits.write",
] as const;
export type Capability = (typeof CAPABILITIES)[number];

const READS: Capability[] = CAPABILITIES.filter((c) => c.endsWith(".read"));

const TABLE: Record<Role, readonly Capability[]> = {
  admin: CAPABILITIES,
  auditor: READS,
  // Who the usage is of, and the group names quotas are set on, come with the job; nothing about their accounts' sign-in.
  billing: ["usage.read", "quotas.read", "quotas.write", "users.read", "directory.read", "limits.read"],
  user: [],
};

export function normalizeRole(r: string | null | undefined): Role {
  return (ROLES as readonly string[]).includes(r ?? "") ? (r as Role) : "user";
}

export function capabilitiesOf(role: Role): readonly Capability[] {
  return TABLE[role] ?? [];
}

export function can(user: { role: string }, cap: Capability): boolean {
  return capabilitiesOf(normalizeRole(user.role)).includes(cap);
}

export function need(user: { role: string }, cap: Capability): void {
  if (!can(user, cap)) throw new ApiError("permission", `Your role does not allow this (${cap})`);
}

/** A role name from the outside, or invalid_request. */
export function parseRole(v: string): Role {
  if (!(ROLES as readonly string[]).includes(v)) throw new ApiError("invalid_request", `Unknown role ${JSON.stringify(v)}`);
  return v as Role;
}

export interface RoleMapping {
  group: string;
  role: Role;
}

/** Validates and cleans group to role rules. */
export function cleanMappings(input: { group?: unknown; role?: unknown }[] | undefined): RoleMapping[] {
  const out: RoleMapping[] = [];
  for (const m of input ?? []) {
    const group = typeof m.group === "string" ? m.group.trim() : "";
    if (!group) continue;
    out.push({ group, role: parseRole(String(m.role)) });
  }
  return out;
}

/** The first rule a member of `groups` matches, by `same` (group names differ in comparison rules by source). */
export function roleFromGroups(rules: RoleMapping[], groups: string[], same: (a: string, b: string) => boolean = (a, b) => a === b): Role {
  for (const r of rules) if (groups.some((g) => same(g, r.group))) return r.role;
  return "user";
}
