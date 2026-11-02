/**
 * MCP server configuration: validation, canonical form, config_hash and `${secret:NAME}` substitution.
 * Pure functions, no database access.
 */
import { createHash } from "node:crypto";
import { Scope } from "../gen/harness/common/v1/scope_pb.ts";
import { Transport } from "../gen/harness/mcp/v1/mcp_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { PROJECT_KEY_RE } from "../services/sync.ts";

export interface StdioCfg {
  command: string;
  args: string[];
  env: Record<string, string>;
  cwd: string;
}

export interface OAuthCfg {
  clientId: string;
  scopes: string[];
  authorizationServer: string;
}

export interface RemoteCfg {
  url: string;
  headers: Record<string, string>;
  oauth?: OAuthCfg;
  requiresUserInteraction: boolean;
}

export interface OverrideCfg {
  deviceId: string;
  os: string;
  command: string;
  args: string[];
  env: Record<string, string>;
}

/** What mcp_server.config stores. Never contains secret values. */
export interface StoredConfig {
  stdio?: StdioCfg;
  remote?: RemoteCfg;
  overrides: OverrideCfg[];
}

/** Writable fields of a server (structurally compatible with harness.mcp.v1.Server). */
export interface ServerInput {
  name: string;
  scope: Scope;
  transport: Transport;
  enabled: boolean;
  alwaysLoad: boolean;
  stdio?: Partial<StdioCfg>;
  remote?: Partial<Omit<RemoteCfg, "oauth">> & { oauth?: Partial<OAuthCfg> };
  overrides: Partial<OverrideCfg>[];
  targetRoles: string[];
  projectKey: string;
}

export interface SecretChangesInput {
  set: Record<string, string>;
  remove: string[];
}

export const REMOTE_TRANSPORTS = new Set([Transport.STREAMABLE_HTTP, Transport.SSE, Transport.WS]);

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;
export const SECRET_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const SECRET_REF_RE = /\$\{secret:([^}]*)\}/g;
const HEADER_RE = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/;
const ENV_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const MAX_SECRET_BYTES = 64 << 10;
const MAX_SECRETS = 64;
const MAX_STR = 8192;

function str(v: unknown, field: string, max = MAX_STR): string {
  const s = typeof v === "string" ? v : "";
  if (s.length > max) throw new ApiError("invalid_request", `${field} is too long`);
  if (s.includes("\0")) throw new ApiError("invalid_request", `${field} must not contain NUL`);
  return s;
}

function strList(v: unknown, field: string, maxItems = 256): string[] {
  const list = Array.isArray(v) ? v : [];
  if (list.length > maxItems) throw new ApiError("invalid_request", `${field} has too many entries`);
  return list.map((x, i) => str(x, `${field}[${i}]`));
}

function strMap(v: unknown, field: string, keyRe: RegExp): Record<string, string> {
  const out: Record<string, string> = {};
  const entries = v && typeof v === "object" ? Object.entries(v as Record<string, unknown>) : [];
  if (entries.length > 128) throw new ApiError("invalid_request", `${field} has too many entries`);
  for (const [k, val] of entries) {
    if (!keyRe.test(k)) throw new ApiError("invalid_request", `Invalid ${field} key ${JSON.stringify(k)}`);
    out[k] = str(val, `${field}.${k}`);
  }
  return out;
}

/** Validates the writable fields; returns the stored config plus normalized top-level fields. */
export function validateServer(input: ServerInput | undefined, isAdmin: boolean) {
  if (!input) throw new ApiError("invalid_request", "server is required");
  if (!NAME_RE.test(input.name ?? "")) {
    throw new ApiError("invalid_request", "name must be 1-64 characters of [A-Za-z0-9_.-], starting with a letter or digit");
  }
  if (![Scope.USER, Scope.ORG, Scope.PROJECT].includes(input.scope)) throw new ApiError("invalid_request", "scope is required");
  if (input.scope === Scope.ORG && !isAdmin) throw new ApiError("permission", "Only admins can manage org MCP servers");
  let projectKey = "";
  if (input.scope === Scope.PROJECT) {
    projectKey = input.projectKey ?? "";
    if (!projectKey || projectKey.length > 300 || !PROJECT_KEY_RE.test(projectKey)) {
      throw new ApiError("invalid_request", "project_key must be a normalized remote (host/owner/repo) or path:<sha256>");
    }
  } else if (input.projectKey) {
    throw new ApiError("invalid_request", "project_key is only allowed with project scope");
  }
  let targetRoles: string[] = [];
  if (input.targetRoles?.length) {
    if (input.scope !== Scope.ORG) throw new ApiError("invalid_request", "target_roles are only allowed at org scope");
    for (const r of input.targetRoles) if (!r || r.length > 50) throw new ApiError("invalid_request", "Invalid target role");
    targetRoles = [...new Set(input.targetRoles)].sort();
  }

  const config: StoredConfig = { overrides: [] };
  if (input.transport === Transport.STDIO) {
    if (input.remote) throw new ApiError("invalid_request", "remote must not be set for stdio servers");
    const command = str(input.stdio?.command, "stdio.command");
    if (!command.trim()) throw new ApiError("invalid_request", "stdio.command is required");
    config.stdio = {
      command,
      args: strList(input.stdio?.args, "stdio.args"),
      env: strMap(input.stdio?.env, "stdio.env", ENV_RE),
      cwd: str(input.stdio?.cwd, "stdio.cwd"),
    };
    if ((input.overrides?.length ?? 0) > 32) throw new ApiError("invalid_request", "At most 32 overrides");
    config.overrides = (input.overrides ?? []).map((o, i) => {
      const ov: OverrideCfg = {
        deviceId: str(o.deviceId, `overrides[${i}].device_id`, 100),
        os: str(o.os, `overrides[${i}].os`, 20),
        command: str(o.command, `overrides[${i}].command`),
        args: strList(o.args, `overrides[${i}].args`),
        env: strMap(o.env, `overrides[${i}].env`, ENV_RE),
      };
      if (!ov.deviceId && !ov.os) throw new ApiError("invalid_request", `overrides[${i}] needs device_id or os`);
      return ov;
    });
  } else if (REMOTE_TRANSPORTS.has(input.transport)) {
    if (input.stdio) throw new ApiError("invalid_request", "stdio must not be set for remote servers");
    if (input.overrides?.length) throw new ApiError("invalid_request", "overrides are only allowed for stdio servers");
    const url = str(input.remote?.url, "remote.url");
    validateRemoteUrl(url, input.transport);
    const oauth = input.remote?.oauth;
    config.remote = {
      url,
      headers: strMap(input.remote?.headers, "remote.headers", HEADER_RE),
      requiresUserInteraction: !!input.remote?.requiresUserInteraction,
    };
    if (oauth && (oauth.clientId || oauth.scopes?.length || oauth.authorizationServer)) {
      const authorizationServer = str(oauth.authorizationServer, "remote.oauth.authorization_server");
      if (authorizationServer) {
        let u: URL;
        try {
          u = new URL(authorizationServer);
        } catch {
          throw new ApiError("invalid_request", "remote.oauth.authorization_server must be a URL");
        }
        if (u.protocol !== "https:" && u.protocol !== "http:") throw new ApiError("invalid_request", "remote.oauth.authorization_server must be http(s)");
      }
      config.remote.oauth = {
        clientId: str(oauth.clientId, "remote.oauth.client_id", 500),
        scopes: [...new Set(strList(oauth.scopes, "remote.oauth.scopes", 64).filter(Boolean))].sort(),
        authorizationServer,
      };
    }
  } else {
    throw new ApiError("invalid_request", "transport is required");
  }
  for (const name of secretRefs(config)) {
    if (!SECRET_NAME_RE.test(name)) throw new ApiError("invalid_request", `Invalid secret reference \${secret:${name}}`);
  }
  return {
    name: input.name,
    scope: input.scope,
    projectKey,
    transport: input.transport,
    enabled: !!input.enabled,
    alwaysLoad: !!input.alwaysLoad,
    targetRoles,
    config,
  };
}

function validateRemoteUrl(url: string, transport: Transport): void {
  if (!url) throw new ApiError("invalid_request", "remote.url is required");
  let u: URL;
  try {
    // Secret references may sit in the URL (e.g. a token in the query); they are substituted when connecting.
    u = new URL(url.replace(SECRET_REF_RE, "x"));
  } catch {
    throw new ApiError("invalid_request", "remote.url must be an absolute URL");
  }
  const ok = transport === Transport.WS ? ["ws:", "wss:"] : ["http:", "https:"];
  if (!ok.includes(u.protocol)) throw new ApiError("invalid_request", `remote.url must use ${ok.join(" or ")}`);
  if (u.username || u.password) throw new ApiError("invalid_request", "Credentials in remote.url are not allowed; use headers with ${secret:NAME}");
}

/** Validates write-only secret changes. */
export function validateSecretChanges(ch: SecretChangesInput | undefined): SecretChangesInput {
  const set: Record<string, string> = {};
  for (const [k, v] of Object.entries(ch?.set ?? {})) {
    if (!SECRET_NAME_RE.test(k) || k.length > 100) throw new ApiError("invalid_request", `Invalid secret name ${JSON.stringify(k)}`);
    if (typeof v !== "string" || Buffer.byteLength(v) > MAX_SECRET_BYTES) throw new ApiError("invalid_request", `Secret ${k} is too large`);
    if (v.includes("\0")) throw new ApiError("invalid_request", `Secret ${k} must not contain NUL`);
    set[k] = v;
  }
  const remove = [...new Set(ch?.remove ?? [])];
  for (const k of remove) {
    if (!SECRET_NAME_RE.test(k)) throw new ApiError("invalid_request", `Invalid secret name ${JSON.stringify(k)}`);
    if (k in set) throw new ApiError("invalid_request", `Secret ${k} is both set and removed`);
  }
  if (Object.keys(set).length > MAX_SECRETS) throw new ApiError("invalid_request", `At most ${MAX_SECRETS} secrets`);
  return { set, remove };
}

/** Every string of the config that may carry `${secret:NAME}` references. */
function referencingStrings(c: StoredConfig): string[] {
  const out: string[] = [];
  if (c.stdio) out.push(c.stdio.command, ...c.stdio.args, ...Object.values(c.stdio.env), c.stdio.cwd);
  if (c.remote) out.push(c.remote.url, ...Object.values(c.remote.headers));
  for (const o of c.overrides) out.push(o.command, ...o.args, ...Object.values(o.env));
  return out;
}

/** Secret names referenced by the config, sorted. */
export function secretRefs(c: StoredConfig): string[] {
  const names = new Set<string>();
  for (const s of referencingStrings(c)) for (const m of s.matchAll(SECRET_REF_RE)) names.add(m[1]!);
  return [...names].sort();
}

/** Replaces `${secret:NAME}`; a missing secret is a failed precondition (the user must set it first). */
export function substitute(s: string, secrets: Map<string, string>): string {
  return s.replace(SECRET_REF_RE, (_, name: string) => {
    const v = secrets.get(name);
    if (v === undefined) throw new ApiError("failed_precondition", `Secret ${name} is not set`);
    return v;
  });
}

export function substituteMap(m: Record<string, string>, secrets: Map<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(m).map(([k, v]) => [k, substitute(v, secrets)]));
}

/** JSON with object keys sorted recursively (arrays keep their order). */
export function canonicalJson(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(canonicalJson).join(",")}]`;
  if (v && typeof v === "object") {
    const keys = Object.keys(v as object)
      .filter((k) => (v as Record<string, unknown>)[k] !== undefined)
      .sort();
    return `{${keys.map((k) => `${JSON.stringify(k)}:${canonicalJson((v as Record<string, unknown>)[k])}`).join(",")}}`;
  }
  return JSON.stringify(v ?? null);
}

/**
 * config_hash: lowercase hex SHA-256 of "harness-mcp-config-v1\0" + canonical JSON of
 * {transport, stdio: {command, args, env, cwd}, remote: {url, headers}, overrides, secrets: [names]}.
 * OAuth hints, enabled / always_load and secret values are not part of it.
 */
export function configHash(transport: Transport, c: StoredConfig, secretNames: string[]): string {
  const body = {
    transport,
    stdio: c.stdio ?? null,
    remote: c.remote ? { url: c.remote.url, headers: c.remote.headers } : null,
    overrides: c.overrides,
    secrets: [...new Set(secretNames)].sort(),
  };
  return createHash("sha256").update(`harness-mcp-config-v1\0${canonicalJson(body)}`).digest("hex");
}

/** Origin of a remote URL (ws URLs are compared as such); "" when not parseable. */
export function urlOrigin(url: string | undefined): string {
  if (!url) return "";
  try {
    return new URL(url.replace(SECRET_REF_RE, "x")).origin;
  } catch {
    return "";
  }
}
