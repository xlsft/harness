/**
 * The organization's managed policy (harness.auth.v1.PolicyService): an admin-set settings layer in the org-scope POLICY
 * document "default", next to the permission mode and local model policies that already live there. It travels to clients
 * through their sync feed (settings level "policy", which always wins over user, project and local settings).
 *
 * Keys of the document:
 *   permissions.allow / ask / deny, allowManagedPermissionRulesOnly    enforced by clients (packages/client/settings)
 *   sandbox.enabled true, sandbox.allowOutside false                   enforced by clients
 *   allowedProviders, disablePersonalProviders                         enforced here too: the gateway serves only allowed models
 *   allowedMcpServers                                                  enforced here too: the catalog, proxy and stdio launches
 * The server cannot be talked out of what it enforces itself, whatever a modified client does; permission rules and the
 * sandbox can only live on the client, because that is where commands run.
 */
import { and, eq, isNull } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { DocumentKind } from "../gen/harness/sync/v1/sync_pb.ts";
import { ApiError } from "../lib/errors.ts";
import type { Principal, PublicUser } from "./devices.ts";
import { getDocument, putDocument, type DocRef, type DocRow } from "./sync.ts";

const POLICY_REF: DocRef = { scope: Scope.ORG, kind: DocumentKind.POLICY, key: "default", projectKey: "" };

type Json = Record<string, unknown>;

export interface ManagedPolicy {
  allow_rules: string[];
  ask_rules: string[];
  deny_rules: string[];
  managed_rules_only: boolean;
  sandbox_required: boolean;
  sandbox_outside_refused: boolean;
  restrict_providers: boolean;
  allowed_providers: string[];
  personal_providers_disabled: boolean;
  restrict_mcp_servers: boolean;
  allowed_mcp_servers: string[];
}

const isObject = (v: unknown): v is Json => !!v && typeof v === "object" && !Array.isArray(v);
const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((s): s is string => typeof s === "string" && !!s.trim()).map((s) => s.trim()) : []);

/** The managed part of a POLICY document's content. */
export function managedOf(content: Json): ManagedPolicy {
  const perms = isObject(content.permissions) ? content.permissions : {};
  const sandbox = isObject(content.sandbox) ? content.sandbox : {};
  return {
    allow_rules: strings(perms.allow),
    ask_rules: strings(perms.ask),
    deny_rules: strings(perms.deny),
    managed_rules_only: content.allowManagedPermissionRulesOnly === true,
    sandbox_required: sandbox.enabled === true,
    sandbox_outside_refused: sandbox.allowOutside === false,
    restrict_providers: Array.isArray(content.allowedProviders),
    allowed_providers: strings(content.allowedProviders),
    personal_providers_disabled: content.disablePersonalProviders === true,
    restrict_mcp_servers: Array.isArray(content.allowedMcpServers),
    allowed_mcp_servers: strings(content.allowedMcpServers),
  };
}

function setOrDelete(o: Json, key: string, value: unknown, on: boolean): void {
  if (on) o[key] = value;
  else delete o[key];
}

/** The document's content with the managed fields replaced; other keys stay. */
export function withManaged(content: Json, p: ManagedPolicy): Json {
  const next: Json = { ...content };
  const perms: Json = isObject(content.permissions) ? { ...content.permissions } : {};
  setOrDelete(perms, "allow", p.allow_rules, p.allow_rules.length > 0);
  setOrDelete(perms, "ask", p.ask_rules, p.ask_rules.length > 0);
  setOrDelete(perms, "deny", p.deny_rules, p.deny_rules.length > 0);
  setOrDelete(next, "permissions", perms, Object.keys(perms).length > 0);
  const sandbox: Json = isObject(content.sandbox) ? { ...content.sandbox } : {};
  setOrDelete(sandbox, "enabled", true, p.sandbox_required);
  setOrDelete(sandbox, "allowOutside", false, p.sandbox_outside_refused);
  setOrDelete(next, "sandbox", sandbox, Object.keys(sandbox).length > 0);
  setOrDelete(next, "allowManagedPermissionRulesOnly", true, p.managed_rules_only);
  setOrDelete(next, "allowedProviders", p.allowed_providers, p.restrict_providers);
  setOrDelete(next, "disablePersonalProviders", true, p.personal_providers_disabled);
  setOrDelete(next, "allowedMcpServers", p.allowed_mcp_servers, p.restrict_mcp_servers);
  return next;
}

/** A permission rule: a tool name, optionally with a specifier in parentheses. */
const RULE = /^[A-Za-z_][\w:*.-]*(\(.*\))?$/s;

export function cleanManaged(p: ManagedPolicy): ManagedPolicy {
  const list = (v: string[]) => [...new Set(strings(v))];
  const out: ManagedPolicy = {
    ...p,
    allow_rules: list(p.allow_rules),
    ask_rules: list(p.ask_rules),
    deny_rules: list(p.deny_rules),
    allowed_providers: list(p.allowed_providers),
    allowed_mcp_servers: list(p.allowed_mcp_servers),
  };
  for (const r of [...out.allow_rules, ...out.ask_rules, ...out.deny_rules]) {
    if (!RULE.test(r)) throw new ApiError("invalid_request", `Not a permission rule: ${r}`);
  }
  return out;
}

async function readDoc(p: Principal): Promise<DocRow | undefined> {
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

export async function getManagedPolicy(p: Principal): Promise<{ policy: ManagedPolicy; updated_at: string }> {
  const row = await readDoc(p);
  return { policy: managedOf(parse(row)), updated_at: row?.updatedAt?.toISOString() ?? "" };
}

/** Admins only; the caller checks. Retries a concurrent write of the same document. */
export async function setManagedPolicy(p: Principal, input: ManagedPolicy): Promise<{ policy: ManagedPolicy; updated_at: string }> {
  const policy = cleanManaged(input);
  for (let attempt = 0; attempt < 5; attempt++) {
    const row = await readDoc(p);
    const r = await putDocument(p, {
      ref: POLICY_REF,
      content: JSON.stringify(withManaged(parse(row), policy)),
      files: [],
      baseVersion: row?.version ?? 0,
      source: "admin:managed-policy",
      targetRoles: (row?.targetRoles as string[] | undefined) ?? [],
    });
    if ("document" in r) return { policy: managedOf(parse(r.document)), updated_at: r.document.updatedAt.toISOString() };
  }
  throw new ApiError("conflict", "The policy changed concurrently; try again");
}

// --- what the server enforces ---------------------------------------------------------------------------

export interface ServerPolicy {
  /** Undefined: no restriction. */
  allowedProviders?: string[];
  personalProvidersDisabled: boolean;
  allowedMcpServers?: string[];
}

function intersect(a: string[] | undefined, b: string[]): string[] {
  return a ? a.filter((x) => b.includes(x)) : b;
}

/** The POLICY documents aimed at this user, merged: lists intersect, restrictions add up (like the client's merge). */
export async function serverPolicyFor(user: Pick<PublicUser, "role">): Promise<ServerPolicy> {
  await ensureDb();
  const d = schema.syncDocument;
  const rows = await getDb()
    .select({ content: d.content, targetRoles: d.targetRoles })
    .from(d)
    .where(and(eq(d.scope, Scope.ORG), eq(d.kind, DocumentKind.POLICY), isNull(d.deletedAt)));
  const out: ServerPolicy = { personalProvidersDisabled: false };
  for (const r of rows) {
    const roles = Array.isArray(r.targetRoles) ? (r.targetRoles as string[]) : [];
    if (roles.length && !roles.includes(user.role)) continue;
    const m = managedOf(parse({ content: r.content } as DocRow));
    if (m.restrict_providers) out.allowedProviders = intersect(out.allowedProviders, m.allowed_providers);
    if (m.restrict_mcp_servers) out.allowedMcpServers = intersect(out.allowedMcpServers, m.allowed_mcp_servers);
    out.personalProvidersDisabled ||= m.personal_providers_disabled;
  }
  return out;
}

export function providerAllowed(p: ServerPolicy, provider: { id: string; presetId?: string | null; ownerUserId?: string | null }): boolean {
  if (provider.ownerUserId && p.personalProvidersDisabled) return false;
  if (!p.allowedProviders) return true;
  return p.allowedProviders.includes(provider.id) || (!!provider.presetId && p.allowedProviders.includes(provider.presetId));
}

export function mcpAllowed(p: ServerPolicy, name: string): boolean {
  return !p.allowedMcpServers || p.allowedMcpServers.includes(name);
}
