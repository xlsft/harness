/**
 * Connect implementation of harness.auth.v1.RoleService (services/roles.ts), and the conversions every other service
 * uses for roles on the wire: a role id string (role_id) with the User.Role enum kept for older readers.
 */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { canAny, isBuiltin, need, normalizeRole, parseRole, PERMISSION_CATALOG, roleName, type Role } from "../auth/roles.ts";
import { User_Role } from "../gen/harness/auth/v1/auth_pb.ts";
import { RoleService } from "../gen/harness/auth/v1/roles_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { createRole, deleteRole, listRoles, updateRole, type RoleView } from "../services/roles.ts";
import { requirePrincipal } from "./context.ts";
import { guard } from "./errors.ts";

/** The enum for a role id; a custom role is ROLE_UNSPECIFIED (readers that know role_id use that). */
export function roleToProto(r: string): User_Role {
  return { admin: User_Role.ADMIN, auditor: User_Role.AUDITOR, billing: User_Role.BILLING, user: User_Role.USER }[r] ?? (isBuiltin(r) ? User_Role.USER : User_Role.UNSPECIFIED);
}

/** The enum, role id and name of a role, for messages that carry all three. */
export function roleFields(r: string): { role: User_Role; roleId: string; roleName: string } {
  return { role: roleToProto(r), roleId: r, roleName: roleName(r) };
}

/** A role from the wire: role_id when set (it must exist), else the enum (unspecified is "user"). */
export function roleFromProto(r: User_Role, roleId = ""): Role {
  if (roleId) return parseRole(roleId);
  if (r === User_Role.ADMIN) return "admin";
  if (r === User_Role.AUDITOR) return "auditor";
  if (r === User_Role.BILLING) return "billing";
  if (r === User_Role.USER || r === User_Role.UNSPECIFIED) return "user";
  throw new ApiError("invalid_request", "Unknown role");
}

export const mappingToProto = (m: { group: string; role: string }) => {
  const id = normalizeRole(m.role);
  return { group: m.group, role: roleToProto(id), roleId: id };
};
export const mappingFromProto = (m: { group: string; role: User_Role; roleId: string }) => ({ group: m.group, role: roleFromProto(m.role, m.roleId) });

function viewToProto(r: RoleView) {
  return {
    id: r.id,
    name: r.name,
    description: r.description,
    builtin: r.builtin,
    editable: r.editable,
    permissions: r.permissions,
    memberCount: r.member_count,
    createdAt: r.created_at,
    updatedAt: r.updated_at,
  };
}

export function registerRoleService(router: ConnectRouter): void {
  router.service(RoleService, {
    listPermissions: guard(async (_req, ctx: HandlerContext) => {
      await requirePrincipal(ctx);
      return {
        permissions: PERMISSION_CATALOG.map((p) => ({ id: p.id, area: p.area, areaLabel: p.area_label, label: p.label, description: p.description })),
      };
    }),
    listRoles: guard(async (_req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      // Who manages users or quotas needs the role names to do it.
      if (!canAny(user, ["roles.read", "users.read", "quotas.read"])) need(user, "roles.read");
      return { roles: (await listRoles(user)).map(viewToProto) };
    }),
    createRole: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      need(user, "roles.write");
      return { role: viewToProto(await createRole(user, { name: req.name, description: req.description, permissions: req.permissions })) };
    }),
    updateRole: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      need(user, "roles.write");
      if (!req.id) throw new ApiError("invalid_request", "id is required");
      const r = await updateRole(user, req.id, {
        name: req.name,
        description: req.description,
        permissions: req.setPermissions ? req.permissions : undefined,
      });
      return { role: viewToProto(r) };
    }),
    deleteRole: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      need(user, "roles.write");
      if (!req.id) throw new ApiError("invalid_request", "id is required");
      const r = await deleteRole(user, req.id, req.moveMembersToUser);
      return { movedMembers: r.moved };
    }),
  });
}
