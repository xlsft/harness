# Roles

Roles decide who administers a server of xlsft`s harness. Everyone uses the product itself the same way (models,
sessions, projects of their own); a role only grants administration.

## Model

- A **permission** is an id from a fixed catalog (`packages/core/src/auth/roles.ts`, `PERMISSION_CATALOG`), grouped by
  area: users, roles, groups and provisioning, usage and quotas, audit, policy, sign-in, providers, provider limits,
  MCP servers, always-on hosts, projects, connections. Nothing outside the catalog can be granted, and an id never
  changes meaning once released.
- A **role** is a named set of permissions. Four are built in, with fixed ids and fixed sets:
  - `admin`: every permission. The last active admin cannot lose it.
  - `auditor`: reads what the server is administered with and changes nothing.
  - `billing`: reads usage and sets spend quotas.
  - `user`: no administration. A role id the server does not know is treated as `user` (deny by default).
- **Custom roles** have `role_…` ids and live in the `custom_role` table (migration `0025_custom_roles`).
- A user has one role. `role_id` on the user wins over the older `role` enum, which stays for built-ins and older
  clients; a custom role reads as `ROLE_UNSPECIFIED` in the enum.

## Checks

Every administrative entry point names the permission it needs and calls `need()` (throws) or `can()` (filters). The
checks are synchronous: each process keeps a mirror of `custom_role` (`services/roles.ts`), reloaded when it is older
than 5 seconds and at once after a change the process made itself, so another replica sees a change within seconds.

## Rules for changing roles

- Names are unique ignoring case, built-in names included.
- Built-ins are neither changed nor deleted.
- Nobody creates, changes or deletes a role with a permission they do not hold; changing one needs both what it grants
  now and what it will grant.
- Deleting a role somebody has needs the caller's confirmation (`move_members_to_user`), and moves them to `user` first.
- Every change is audited (`role.create`, `role.update`, `role.delete`).

## Where roles are used

- **Settings → Roles** lists roles with their permissions (`roles.read`) and edits custom ones (`roles.write`).
- Role pickers in Users, group-to-role rules (LDAP, SSO, SCIM), the two-factor policy and per-role quotas list every
  role the server has (`useRoles()` in the UI); they fall back to the four built-ins on a server from before custom
  roles.
- `RoleService` (`proto/harness/auth/v1/roles.proto`): `ListPermissions`, `ListRoles`, `CreateRole`, `UpdateRole`,
  `DeleteRole`.
