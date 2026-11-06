/**
 * Proxy for remote MCP servers (streamable_http, sse, ws) using the official SDK client.
 *
 * Pooling: one upstream MCP session per (server, user), because tokens and the tool list are per user.
 * An entry is reused while its fingerprint (config_hash, private-network flag) matches and it is open;
 * it is closed after HARNESS_MCP_IDLE_MS without use (default 5 min), when the server is updated or
 * deleted, and when the user's grant changes. Concurrent requests share the session (MCP multiplexes).
 *
 * Credentials never leave the server: static headers / URL secrets are substituted here and the OAuth
 * access token is injected per HTTP request by a fetch wrapper, which refreshes it before expiry and
 * retries once with a refreshed token on 401. A 401 that survives marks the grant NEEDS_AUTH.
 *
 * Every outbound connection goes through the SSRF guard (lib/ssrf.ts) unless the admin set
 * allow_private_network on the server.
 *
 * Elicitation: an upstream's elicitation/create goes to the newest running tools/call of the pooled session (the
 * protocol does not say which one it belongs to, and sessions are shared), as an event on that call's stream; the caller
 * answers with RespondToElicitation. While it waits, the call's time limit stands still. Without a running call the
 * request is cancelled: nobody could answer it.
 *
 * Health: per (server, user) consecutive failures; after 3 failures new connections are refused for an
 * exponentially growing window (max 60 s) so a dead upstream does not stall every caller.
 */
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import type { Transport as McpTransport } from "@modelcontextprotocol/sdk/shared/transport.js";
import {
  CallToolResultSchema,
  ElicitRequestSchema,
  ErrorCode,
  GetPromptResultSchema,
  ListPromptsResultSchema,
  ListResourcesResultSchema,
  ListResourceTemplatesResultSchema,
  ListToolsResultSchema,
  McpError,
  PromptListChangedNotificationSchema,
  ReadResourceResultSchema,
  ResourceListChangedNotificationSchema,
  ToolListChangedNotificationSchema,
  type Tool as McpTool,
} from "@modelcontextprotocol/sdk/types.js";
import { and, eq } from "drizzle-orm";
import { ensureDb, getDb, schema } from "../db/index.ts";
import { Transport } from "../gen/harness/mcp/v1/mcp_pb.ts";
import { ApiError } from "../lib/errors.ts";
import { newId } from "../lib/ids.ts";
import { assertUrlAllowed, guardedFetchFn, ssrfError } from "../lib/ssrf.ts";
import type { PublicUser } from "../services/devices.ts";
import { MAX_TOOL_TIMEOUT_MS, REMOTE_TRANSPORTS, substitute, substituteMap, toolVisible } from "./config.ts";
import { assertAuthorized, getAccessToken, markNeedsAuth, onAuthChanged } from "./oauth.ts";
import { loadSecrets, onServerChanged, rowConfig, type ServerRow, type ServerView } from "./registry.ts";
import { GuardedWebSocketTransport } from "./ws-transport.ts";

/** Tool as stored in caches and catalogs (JSON values, serialized to strings at the RPC edge). */
export interface ToolData {
  name: string;
  title: string;
  description: string;
  inputSchema: unknown;
  outputSchema?: unknown;
  annotations?: Record<string, unknown>;
}

/** What the upstream says about itself, kept with its tool list. */
export interface UpstreamMeta {
  instructions?: string;
  hasPrompts?: boolean;
  hasResources?: boolean;
}

export type ListKind = "tools" | "prompts" | "resources";

export interface ElicitationEvent {
  id: string;
  mode: string;
  message: string;
  requestedSchemaJson: string;
  url: string;
}

type ElicitAnswer = { action: "accept" | "decline" | "cancel"; content?: Record<string, unknown> };

/** A running tools/call of a pooled session. */
interface RunningCall {
  onElicit(e: ElicitationEvent): void;
  onListChanged(kind: ListKind): void;
  pause(): void;
  resume(): void;
  /** Elicitations waiting for this call's answer. */
  waiting: Set<string>;
}

interface Entry {
  key: string;
  serverId: string;
  userId: string;
  fingerprint: string;
  client: Client;
  transport: McpTransport;
  lastUsed: number;
  inflight: number;
  closed: boolean;
  /** Set by the fetch wrapper when the upstream answered 401 even after a refresh. */
  unauthorized: boolean;
  calls: RunningCall[];
}

const pendingElicitations = new Map<string, { userId: string; resolve: (a: ElicitAnswer) => void; call: RunningCall }>();

interface Health {
  failures: number;
  lastError: string;
  lastFailureAt: number;
  lastOkAt: number;
}

const pool = new Map<string, { fingerprint: string; entry: Promise<Entry> }>();
const health = new Map<string, Health>();
let sweeper: NodeJS.Timeout | undefined;

function intEnv(name: string, def: number): number {
  const n = Number(process.env[name] ?? "");
  return Number.isFinite(n) && n > 0 ? n : def;
}

const idleMs = () => intEnv("HARNESS_MCP_IDLE_MS", 300_000);
const requestTimeoutMs = () => intEnv("HARNESS_MCP_REQUEST_TIMEOUT_MS", 60_000);
const DEFAULT_CALL_TIMEOUT_MS = 600_000;

const keyOf = (serverId: string, userId: string) => `${serverId}\0${userId}`;

// --- pool maintenance ----------------------------------------------------------------------------

function ensureSweeper(): void {
  if (sweeper) return;
  sweeper = setInterval(sweepIdle, Math.min(30_000, Math.max(250, Math.floor(idleMs() / 2))));
  sweeper.unref();
}

/** Closes connections idle for longer than HARNESS_MCP_IDLE_MS. Exported for tests. */
export function sweepIdle(now = Date.now()): void {
  for (const [key, slot] of pool) {
    slot.entry.then(
      (e) => {
        if (e.inflight === 0 && now - e.lastUsed >= idleMs() && pool.get(key) === slot) {
          pool.delete(key);
          void closeEntry(e);
        }
      },
      () => pool.get(key) === slot && pool.delete(key),
    );
  }
}

async function closeEntry(e: Entry): Promise<void> {
  if (e.closed) return;
  e.closed = true;
  try {
    // Streamable HTTP: end the upstream session (DELETE) so the server can free it.
    if (e.transport instanceof StreamableHTTPClientTransport) await e.transport.terminateSession().catch(() => {});
    await e.client.close();
  } catch {
    // ignore
  }
}

function dropWhere(pred: (serverId: string, userId: string) => boolean): void {
  for (const [key, slot] of [...pool]) {
    const [serverId, userId] = key.split("\0") as [string, string];
    if (!pred(serverId, userId)) continue;
    pool.delete(key);
    slot.entry.then((e) => (e.inflight === 0 ? closeEntry(e) : ((e.closed = true), undefined)), () => {});
  }
}

onServerChanged((id) => dropWhere((s) => s === id));
onAuthChanged((id, user) => dropWhere((s, u) => s === id && u === user));

/** Closes every pooled connection (tests, shutdown). */
export async function closeAllConnections(): Promise<void> {
  const all = [...pool.values()];
  pool.clear();
  health.clear();
  await Promise.all(all.map((s) => s.entry.then(closeEntry, () => {})));
}

/** Number of open pooled sessions, per server when given. */
export async function poolSize(serverId?: string): Promise<number> {
  let n = 0;
  for (const [key, slot] of pool) {
    if (serverId && !key.startsWith(`${serverId}\0`)) continue;
    const e = await slot.entry.catch(() => undefined);
    if (e && !e.closed) n++;
  }
  return n;
}

export function serverHealth(serverId: string, userId: string): Health | undefined {
  return health.get(keyOf(serverId, userId));
}

function recordOk(key: string): void {
  const h = health.get(key);
  health.set(key, { failures: 0, lastError: h?.lastError ?? "", lastFailureAt: h?.lastFailureAt ?? 0, lastOkAt: Date.now() });
}

function recordFailure(key: string, err: unknown): void {
  const h = health.get(key) ?? { failures: 0, lastError: "", lastFailureAt: 0, lastOkAt: 0 };
  health.set(key, { ...h, failures: h.failures + 1, lastError: err instanceof Error ? err.message : String(err), lastFailureAt: Date.now() });
}

function checkCircuit(key: string): void {
  const h = health.get(key);
  if (!h || h.failures < 3) return;
  const window = Math.min(60_000, 1000 * 2 ** (h.failures - 3));
  const wait = h.lastFailureAt + window - Date.now();
  if (wait > 0) throw new ApiError("upstream", `MCP server is unavailable (${h.lastError}); retrying in ${Math.ceil(wait / 1000)} s`, { retryAfterMs: wait });
}

// --- connecting ----------------------------------------------------------------------------------

function fingerprint(row: ServerRow): string {
  return `${row.configHash}:${row.allowPrivateNetwork ? 1 : 0}`;
}

/** StreamableHTTPError / SseError carry the HTTP status in `code`. */
function isUnauthorized(err: unknown): boolean {
  return (err as { code?: unknown } | undefined)?.code === 401;
}

async function connect(user: PublicUser, row: ServerRow): Promise<Entry> {
  const cfg = rowConfig(row);
  if (!cfg.remote || !REMOTE_TRANSPORTS.has(row.transport)) throw new ApiError("invalid_request", "stdio MCP servers run on devices, not through the proxy");
  const secrets = await loadSecrets(row.id);
  const url = new URL(substitute(cfg.remote.url, secrets));
  const headers = substituteMap(cfg.remote.headers, secrets);
  const base = guardedFetchFn(row.allowPrivateNetwork);
  assertUrlAllowed(url.href.replace(/^ws(s?):/i, "http$1:"), row.allowPrivateNetwork);

  const entry: Entry = {
    key: keyOf(row.id, user.id),
    serverId: row.id,
    userId: user.id,
    fingerprint: fingerprint(row),
    client: new Client({ name: "harness", version: "0.1.0" }, { capabilities: { elicitation: { form: {}, url: {} } } }),
    transport: undefined as unknown as McpTransport,
    lastUsed: Date.now(),
    inflight: 0,
    closed: false,
    unauthorized: false,
    calls: [],
  };

  const authFetch = async (input: string | URL, init?: RequestInit): Promise<Response> => {
    const send = async (token: string | undefined) => {
      const h = new Headers(init?.headers);
      for (const [k, v] of Object.entries(headers)) h.set(k, v);
      if (token) h.set("authorization", `Bearer ${token}`);
      return base(input, { ...init, headers: h });
    };
    const token = await getAccessToken(user.id, row);
    let res = await send(token);
    if (res.status === 401 && token) {
      const fresh = await getAccessToken(user.id, row, true);
      if (fresh && fresh !== token) {
        await res.body?.cancel();
        res = await send(fresh);
      }
    }
    if (res.status === 401) entry.unauthorized = true;
    return res;
  };

  let transport: McpTransport;
  if (row.transport === Transport.WS) {
    const token = await getAccessToken(user.id, row);
    transport = new GuardedWebSocketTransport(url, token ? { ...headers, authorization: `Bearer ${token}` } : headers, row.allowPrivateNetwork);
  } else if (row.transport === Transport.SSE) {
    transport = new SSEClientTransport(url, { fetch: authFetch, eventSourceInit: { fetch: authFetch } });
  } else {
    transport = new StreamableHTTPClientTransport(url, { fetch: authFetch });
  }
  entry.transport = transport;
  entry.client.onclose = () => {
    entry.closed = true;
    const slot = pool.get(entry.key);
    if (slot) slot.entry.then((e) => e === entry && pool.get(entry.key) === slot && pool.delete(entry.key), () => {});
  };
  const changed = (kind: ListKind) => {
    for (const call of entry.calls) call.onListChanged(kind);
  };
  entry.client.setNotificationHandler(ToolListChangedNotificationSchema, async () => {
    await invalidateToolCache(row.id, user.id);
    changed("tools");
  });
  entry.client.setNotificationHandler(PromptListChangedNotificationSchema, async () => changed("prompts"));
  entry.client.setNotificationHandler(ResourceListChangedNotificationSchema, async () => changed("resources"));
  entry.client.setRequestHandler(ElicitRequestSchema, async (req) => {
    const call = entry.calls.at(-1);
    if (!call) return { action: "cancel" as const };
    const id = newId("mel");
    const p = req.params as { mode?: string; message: string; requestedSchema?: unknown; url?: string };
    const answer = new Promise<ElicitAnswer>((resolve) => pendingElicitations.set(id, { userId: user.id, resolve, call }));
    call.waiting.add(id);
    call.pause();
    try {
      call.onElicit({
        id,
        mode: p.mode === "url" ? "url" : "form",
        message: p.message,
        requestedSchemaJson: p.requestedSchema ? JSON.stringify(p.requestedSchema) : "",
        url: p.url ?? "",
      });
      return await answer;
    } finally {
      call.waiting.delete(id);
      pendingElicitations.delete(id);
      call.resume();
    }
  });
  try {
    await entry.client.connect(transport, { timeout: requestTimeoutMs() });
  } catch (err) {
    await entry.client.close().catch(() => {});
    throw await mapError(err, user, row, entry);
  }
  return entry;
}

async function mapError(err: unknown, user: PublicUser, row: ServerRow, entry?: Entry): Promise<ApiError> {
  const blocked = ssrfError(err);
  if (blocked) return blocked;
  if (err instanceof ApiError) return err;
  if (entry?.unauthorized || isUnauthorized(err)) {
    await markNeedsAuth(user.id, row, "The server requires authorization");
    return new ApiError("needs_auth", `MCP server ${row.name} needs authorization`);
  }
  if (err instanceof McpError) {
    if (err.code === ErrorCode.MethodNotFound) return new ApiError("invalid_request", `MCP server ${row.name} does not support this: ${err.message}`);
    if (err.code === ErrorCode.InvalidParams) return new ApiError("invalid_request", err.message);
    return new ApiError("upstream", `MCP server ${row.name}: ${err.message}`);
  }
  const msg = err instanceof Error ? err.message : String(err);
  return new ApiError("upstream", `MCP server ${row.name}: ${msg}`);
}

async function acquire(user: PublicUser, row: ServerRow): Promise<Entry> {
  ensureSweeper();
  const key = keyOf(row.id, user.id);
  const fp = fingerprint(row);
  const slot = pool.get(key);
  if (slot && slot.fingerprint === fp) {
    const e = await slot.entry.catch(() => undefined);
    if (e && !e.closed) return e;
    if (pool.get(key) === slot) pool.delete(key);
  } else if (slot) {
    pool.delete(key);
    slot.entry.then(closeEntry, () => {});
  }
  checkCircuit(key);
  const fresh = { fingerprint: fp, entry: connect(user, row) };
  pool.set(key, fresh);
  try {
    const e = await fresh.entry;
    recordOk(key);
    return e;
  } catch (err) {
    if (pool.get(key) === fresh) pool.delete(key);
    if (!(err instanceof ApiError && (err.type === "failed_precondition" || err.type === "needs_auth" || err.type === "permission"))) recordFailure(key, err);
    throw err;
  }
}

/**
 * Runs `fn` with a pooled client. Connection-level failures of idempotent requests are retried once on a
 * fresh connection; tool calls are never retried.
 */
async function withClient<T>(user: PublicUser, view: Pick<ServerView, "row" | "secrets">, idempotent: boolean, fn: (c: Client, e: Entry) => Promise<T>): Promise<T> {
  const row = view.row;
  if (!REMOTE_TRANSPORTS.has(row.transport)) throw new ApiError("invalid_request", "stdio MCP servers run on devices, not through the proxy");
  if (!row.enabled) throw new ApiError("failed_precondition", `MCP server ${row.name} is disabled`);
  await assertAuthorized(user, view);
  for (let attempt = 0; ; attempt++) {
    const e = await acquire(user, row);
    e.inflight++;
    try {
      const r = await fn(e.client, e);
      recordOk(e.key);
      return r;
    } catch (err) {
      const connLost = err instanceof McpError && err.code === ErrorCode.ConnectionClosed && !e.unauthorized;
      if (e.unauthorized || connLost) {
        const slot = pool.get(e.key);
        if (slot && (await slot.entry.catch(() => undefined)) === e) pool.delete(e.key);
        void closeEntry(e);
      }
      if (connLost && idempotent && attempt === 0) continue;
      const mapped = await mapError(err, user, row, e);
      if (mapped.type === "upstream") recordFailure(e.key, mapped);
      throw mapped;
    } finally {
      e.inflight--;
      e.lastUsed = Date.now();
    }
  }
}

// --- tools / prompts / resources -------------------------------------------------------------------

function toToolData(t: McpTool): ToolData {
  return {
    name: t.name,
    title: t.title ?? (t.annotations?.title as string | undefined) ?? "",
    description: t.description ?? "",
    inputSchema: t.inputSchema,
    outputSchema: t.outputSchema,
    annotations: t.annotations as Record<string, unknown> | undefined,
  };
}

/**
 * Tools as presented to clients: upstream annotations unchanged, plus `requiresUserInteraction: true` for
 * servers configured with remote.requires_user_interaction, so a client that only sees the tool (catalog,
 * ToolSearch) still knows it must ask.
 */
export function presentTools(row: ServerRow, tools: ToolData[]): ToolData[] {
  const cfg = rowConfig(row);
  const visible = tools.filter((t) => toolVisible(cfg, t.name));
  if (!cfg.remote?.requiresUserInteraction) return visible;
  return visible.map((t) => ({ ...t, annotations: { ...(t.annotations ?? {}), requiresUserInteraction: true } }));
}

async function invalidateToolCache(serverId: string, userId: string): Promise<void> {
  await ensureDb();
  await getDb().delete(schema.mcpToolCache).where(and(eq(schema.mcpToolCache.serverId, serverId), eq(schema.mcpToolCache.userId, userId)));
}

function upstreamMeta(c: Client): UpstreamMeta {
  const caps = c.getServerCapabilities();
  return { instructions: c.getInstructions() ?? "", hasPrompts: !!caps?.prompts, hasResources: !!caps?.resources };
}

async function storeToolCache(row: ServerRow, userId: string, tools: ToolData[], meta: UpstreamMeta): Promise<void> {
  await ensureDb();
  const values = { configHash: row.configHash, tools, meta, fetchedAt: new Date() };
  await getDb()
    .insert(schema.mcpToolCache)
    .values({ serverId: row.id, userId, ...values })
    .onConflictDoUpdate({ target: [schema.mcpToolCache.serverId, schema.mcpToolCache.userId], set: values });
}

const reqOpts = (signal?: AbortSignal) => ({ timeout: requestTimeoutMs(), signal });

export async function listTools(user: PublicUser, view: ServerView, cursor: string, signal?: AbortSignal) {
  const { r, meta } = await withClient(user, view, true, async (c) => ({
    r: await c.request({ method: "tools/list", params: cursor ? { cursor } : {} }, ListToolsResultSchema, reqOpts(signal)),
    meta: upstreamMeta(c),
  }));
  const tools = r.tools.map(toToolData);
  // A complete first page is the whole list: keep it for the catalog.
  if (!cursor && !r.nextCursor) await storeToolCache(view.row, user.id, tools, meta);
  return { tools: presentTools(view.row, tools), nextCursor: r.nextCursor ?? "" };
}

/** What the upstream says about itself (Extensions > Connectors). */
export async function getServerInfo(user: PublicUser, view: ServerView, signal?: AbortSignal) {
  return withClient(user, view, true, async (c) => {
    // The request proves the session is alive; a pooled session may have died since it connected.
    await c.ping(reqOpts(signal));
    const caps = c.getServerCapabilities();
    const v = c.getServerVersion();
    return { name: v?.name ?? "", version: v?.version ?? "", instructions: c.getInstructions() ?? "", hasTools: !!caps?.tools, hasPrompts: !!caps?.prompts, hasResources: !!caps?.resources };
  });
}

/** All pages of tools/list (catalog refresh); stores the result in the tool cache. */
export async function fetchAllTools(user: PublicUser, view: Pick<ServerView, "row" | "secrets">, signal?: AbortSignal): Promise<{ tools: ToolData[]; meta: UpstreamMeta }> {
  const tools: ToolData[] = [];
  let meta: UpstreamMeta = {};
  let cursor: string | undefined;
  for (let page = 0; page < 50; page++) {
    const r = await withClient(user, view, true, async (c) => {
      const res = await c.request({ method: "tools/list", params: cursor ? { cursor } : {} }, ListToolsResultSchema, reqOpts(signal));
      meta = upstreamMeta(c);
      return res;
    });
    tools.push(...r.tools.map(toToolData));
    cursor = r.nextCursor;
    if (!cursor) break;
  }
  await storeToolCache(view.row, user.id, tools, meta);
  return { tools, meta };
}

export async function listPrompts(user: PublicUser, view: ServerView, cursor: string, signal?: AbortSignal) {
  return withClient(user, view, true, async (c) => {
    if (!c.getServerCapabilities()?.prompts) return { prompts: [], nextCursor: "" };
    const r = await c.request({ method: "prompts/list", params: cursor ? { cursor } : {} }, ListPromptsResultSchema, reqOpts(signal));
    return { prompts: r.prompts, nextCursor: r.nextCursor ?? "" };
  });
}

export async function listResources(user: PublicUser, view: ServerView, cursor: string, signal?: AbortSignal) {
  return withClient(user, view, true, async (c) => {
    if (!c.getServerCapabilities()?.resources) return { resources: [], templates: [], nextCursor: "" };
    const r = await c.request({ method: "resources/list", params: cursor ? { cursor } : {} }, ListResourcesResultSchema, reqOpts(signal));
    const templates = [];
    if (!cursor) {
      let tc: string | undefined;
      for (let page = 0; page < 20; page++) {
        const t = await c
          .request({ method: "resources/templates/list", params: tc ? { cursor: tc } : {} }, ListResourceTemplatesResultSchema, reqOpts(signal))
          .catch((err) => {
            if (err instanceof McpError && err.code === ErrorCode.MethodNotFound) return undefined;
            throw err;
          });
        if (!t) break;
        templates.push(...t.resourceTemplates);
        tc = t.nextCursor;
        if (!tc) break;
      }
    }
    return { resources: r.resources, templates, nextCursor: r.nextCursor ?? "" };
  });
}

export async function getPrompt(user: PublicUser, view: ServerView, name: string, args: Record<string, string>, signal?: AbortSignal) {
  if (!name) throw new ApiError("invalid_request", "name is required");
  return withClient(user, view, true, (c) => c.request({ method: "prompts/get", params: { name, arguments: args } }, GetPromptResultSchema, reqOpts(signal)));
}

export async function readResource(user: PublicUser, view: ServerView, uri: string, signal?: AbortSignal) {
  if (!uri) throw new ApiError("invalid_request", "uri is required");
  return withClient(user, view, true, (c) => c.request({ method: "resources/read", params: { uri } }, ReadResourceResultSchema, reqOpts(signal)));
}

export interface ProgressEvent {
  progress: number;
  total: number;
  message: string;
}

export interface CallEvents {
  onProgress(p: ProgressEvent): void;
  onElicit(e: ElicitationEvent): void;
  onListChanged(kind: ListKind): void;
}

/** Answers an elicitation of one of the user's running calls. */
export function respondToElicitation(user: PublicUser, id: string, action: string, contentJson: string): void {
  const pending = pendingElicitations.get(id);
  // Someone else's id looks like one that does not exist.
  if (!pending || pending.userId !== user.id) throw new ApiError("not_found", "No such elicitation is waiting");
  if (action !== "accept" && action !== "decline" && action !== "cancel") throw new ApiError("invalid_request", "action must be accept, decline or cancel");
  let content: Record<string, unknown> | undefined;
  if (action === "accept" && contentJson) {
    let v: unknown;
    try {
      v = JSON.parse(contentJson);
    } catch {
      throw new ApiError("invalid_request", "content_json must be valid JSON");
    }
    if (!v || typeof v !== "object" || Array.isArray(v)) throw new ApiError("invalid_request", "content_json must be a JSON object");
    content = v as Record<string, unknown>;
  }
  pending.resolve({ action, content });
}

/** A time limit that stands still while paused. */
function pausableTimer(ms: number, fire: () => void) {
  let left = ms;
  let started = Date.now();
  let paused = 0;
  let timer: NodeJS.Timeout | undefined = setTimeout(fire, ms);
  return {
    pause() {
      if (++paused === 1 && timer) {
        clearTimeout(timer);
        timer = undefined;
        left -= Date.now() - started;
      }
    },
    resume() {
      if (--paused === 0) {
        started = Date.now();
        timer = setTimeout(fire, Math.max(0, left));
      }
    },
    stop() {
      if (timer) clearTimeout(timer);
      timer = undefined;
      paused = Number.POSITIVE_INFINITY;
    },
  };
}

/**
 * tools/call. Progress, elicitations and list changes go to `events`; aborting `signal` (the RPC was cancelled)
 * makes the SDK send notifications/cancelled upstream. The call times out after
 * `timeoutMs` (default: the server's tool_timeout_ms, else 10 minutes), not counting the wait for the user's answer
 * to an elicitation.
 */
export async function callTool(
  user: PublicUser,
  view: ServerView,
  toolName: string,
  argumentsJson: string,
  timeoutMs: number,
  signal: AbortSignal,
  events: CallEvents,
): Promise<{ isError: boolean; resultJson: string }> {
  if (!toolName) throw new ApiError("invalid_request", "tool_name is required");
  if (!toolVisible(view.config, toolName)) throw new ApiError("permission", `This server's configuration does not allow the tool ${toolName}`);
  let args: unknown = {};
  if (argumentsJson) {
    try {
      args = JSON.parse(argumentsJson);
    } catch {
      throw new ApiError("invalid_request", "arguments_json must be valid JSON");
    }
  }
  if (!args || typeof args !== "object" || Array.isArray(args)) throw new ApiError("invalid_request", "arguments_json must be a JSON object");
  const limit = Math.min(timeoutMs > 0 ? timeoutMs : view.config.toolTimeoutMs || DEFAULT_CALL_TIMEOUT_MS, MAX_TOOL_TIMEOUT_MS);
  const r = await withClient(user, view, false, async (c, entry) => {
    const expired = new AbortController();
    let timedOut = false;
    const timer = pausableTimer(limit, () => {
      timedOut = true;
      expired.abort();
    });
    const call: RunningCall = { onElicit: events.onElicit, onListChanged: events.onListChanged, pause: timer.pause, resume: timer.resume, waiting: new Set() };
    entry.calls.push(call);
    try {
      return await c.request({ method: "tools/call", params: { name: toolName, arguments: args as Record<string, unknown> } }, CallToolResultSchema, {
        signal: AbortSignal.any([signal, expired.signal]),
        // The timer above is the limit; this only keeps a call from outliving every limit there can be.
        timeout: 2 * MAX_TOOL_TIMEOUT_MS,
        onprogress: (p) => events.onProgress({ progress: p.progress, total: p.total ?? 0, message: p.message ?? "" }),
      });
    } catch (err) {
      if (timedOut) throw new ApiError("upstream", `MCP server ${view.row.name} did not answer within ${Math.round(limit / 1000)} s`);
      throw err;
    } finally {
      timer.stop();
      entry.calls.splice(entry.calls.indexOf(call), 1);
      // The upstream may still be waiting for an answer nobody will give.
      for (const id of call.waiting) pendingElicitations.get(id)?.resolve({ action: "cancel" });
    }
  });
  return { isError: r.isError === true, resultJson: JSON.stringify(r) };
}
