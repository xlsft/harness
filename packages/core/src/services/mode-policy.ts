/**
 * The server-wide permission mode policy (AdminService.Get/SetPermissionModePolicy): the modes nobody may use.
 *
 * It lives in the org-scope POLICY document "default" as `permissions.disabledModes` (settings mode names), so it
 * reaches every signed-in client through its sync feed like the rest of the managed settings; the clients enforce
 * it. Other keys of the document are kept as they are. The older single-mode switches of the same document
 * (`permissions.disableBypassPermissionsMode`, `disableAutoMode`) count as disabled modes too, and are removed
 * when the admin enables that mode again, so the list the admin edits is the whole truth.
 */
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal } from "./devices.ts";
import { getDocument, putDocument, type DocRef, type DocRow } from "./sync.ts";

/** Modes in settings spelling, in the order clients cycle through them (then the ones outside the cycle). */
export const PERMISSION_MODES = ["default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"] as const;
export type ModeName = (typeof PERMISSION_MODES)[number];

/** The mode that can never be disabled. */
export const ALWAYS_ALLOWED: ModeName = "default";

const POLICY_REF: DocRef = { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default", projectKey: "" };

type Json = Record<string, unknown>;

function isObject(v: unknown): v is Json {
  return !!v && typeof v === "object" && !Array.isArray(v);
}

function disableFlag(v: unknown): boolean {
  return v === true || v === "disable";
}

function sortModes(modes: Iterable<ModeName>): ModeName[] {
  return [...new Set(modes)].sort((a, b) => PERMISSION_MODES.indexOf(a) - PERMISSION_MODES.indexOf(b));
}

/** The disabled modes a POLICY document's content declares (unknown names and "default" are ignored). */
export function disabledModesOf(content: Json): ModeName[] {
  const out: ModeName[] = [];
  const perms = isObject(content.permissions) ? content.permissions : {};
  if (Array.isArray(perms.disabledModes)) {
    for (const m of perms.disabledModes) {
      if (typeof m === "string" && (PERMISSION_MODES as readonly string[]).includes(m) && m !== ALWAYS_ALLOWED) out.push(m as ModeName);
    }
  }
  if (perms.disableBypassPermissionsMode === "disable") out.push("bypassPermissions");
  if (disableFlag(perms.disableAutoMode) || disableFlag(content.disableAutoMode)) out.push("auto");
  return sortModes(out);
}

/** Checks an admin's list: known modes only, never "default", and at least one mode left. */
export function validateDisabledModes(modes: string[]): ModeName[] {
  for (const m of modes) {
    if (!(PERMISSION_MODES as readonly string[]).includes(m)) throw new ApiError("invalid_request", `Unknown permission mode ${JSON.stringify(m)}`);
    if (m === ALWAYS_ALLOWED) throw new ApiError("invalid_request", "The default (Ask) permission mode cannot be disabled");
  }
  const out = sortModes(modes as ModeName[]);
  if (out.length >= PERMISSION_MODES.length) throw new ApiError("invalid_request", "At least one permission mode must stay enabled");
  return out;
}

async function readPolicy(p: Principal): Promise<DocRow | undefined> {
  try {
    return await getDocument(p, { ref: POLICY_REF });
  } catch (e) {
    if (e instanceof ApiError && e.type === "not_found") return undefined;
    throw e;
  }
}

function parse(row: DocRow | undefined): Json {
  if (!row) return {};
  try {
    const v: unknown = JSON.parse(row.content);
    return isObject(v) ? v : {};
  } catch {
    return {};
  }
}

export async function getModePolicy(p: Principal): Promise<ModeName[]> {
  return disabledModesOf(parse(await readPolicy(p)));
}

/** Content of the policy document with `disabled` as its mode list, everything else kept. */
export function withDisabledModes(content: Json, disabled: ModeName[]): Json {
  const next: Json = { ...content };
  const perms: Json = isObject(content.permissions) ? { ...content.permissions } : {};
  if (disabled.length) perms.disabledModes = disabled;
  else delete perms.disabledModes;
  if (!disabled.includes("bypassPermissions")) delete perms.disableBypassPermissionsMode;
  if (!disabled.includes("auto")) {
    delete perms.disableAutoMode;
    delete next.disableAutoMode;
  }
  if (Object.keys(perms).length) next.permissions = perms;
  else delete next.permissions;
  return next;
}

/** Replaces the disabled modes (admins only; the caller checks the role). Retries a concurrent write. */
export async function setModePolicy(p: Principal, modes: string[]): Promise<ModeName[]> {
  const disabled = validateDisabledModes(modes);
  for (let attempt = 0; attempt < 5; attempt++) {
    const row = await readPolicy(p);
    const content = parse(row);
    const r = await putDocument(p, {
      ref: POLICY_REF,
      content: JSON.stringify(withDisabledModes(content, disabled)),
      files: [],
      baseVersion: row?.version ?? 0,
      source: "admin:permission-mode-policy",
      targetRoles: (row?.targetRoles as string[] | undefined) ?? [],
    });
    if ("document" in r) return disabledModesOf(parse(r.document));
  }
  throw new ApiError("conflict", "The policy changed concurrently; try again");
}
