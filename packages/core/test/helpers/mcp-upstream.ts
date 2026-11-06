/**
 * Local remote-MCP fixture for the proxy and OAuth tests: one HTTP server on 127.0.0.1 that serves
 * - an MCP server built with the SDK (Streamable HTTP at /mcp, legacy SSE at /sse + /messages) with tools
 *   echo / slow (progress + cancel) / peek (read-only annotations), a prompt and resources + a template;
 * - optionally a fake OAuth 2.1 authorization server at the same origin: RFC 9728 protected resource
 *   metadata, RFC 8414 metadata, dynamic client registration, /authorize (auto-approves) and /token with
 *   PKCE (S256), refresh tokens and configurable access-token lifetime.
 */
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { McpServer, ResourceTemplate } from "@modelcontextprotocol/sdk/server/mcp.js";
import { SSEServerTransport } from "@modelcontextprotocol/sdk/server/sse.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { z } from "zod";

export interface UpstreamOptions {
  /** Require a bearer token issued by the fake authorization server on /mcp and /sse. */
  oauth?: boolean;
  /** expires_in of issued access tokens (seconds). */
  accessTtlSec?: number;
  /** Also serve the tools ask (elicitation) and announce (list_changed notifications). */
  extras?: boolean;
}

export interface Upstream {
  url: string;
  /** Header values seen on MCP requests (lowercased names). */
  seenHeaders: Record<string, string>[];
  /** Tool call ids whose AbortSignal fired (notifications/cancelled received). */
  cancelled: string[];
  stats: { registrations: number; authorizeRequests: URLSearchParams[]; tokenRequests: URLSearchParams[]; refreshes: number; sessions: number };
  /** Invalidates every access token (forces the proxy to refresh). */
  revokeAccessTokens(): void;
  /** Invalidates every refresh token too (forces re-authorization). */
  revokeAll(): void;
  setAccessTtl(sec: number): void;
  close(): Promise<void>;
}

function b64url(buf: Buffer): string {
  return buf.toString("base64url");
}

async function readBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  return Buffer.concat(chunks).toString("utf8");
}

function sendJson(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) {
  res.writeHead(status, { "content-type": "application/json", ...headers });
  res.end(JSON.stringify(body));
}

export async function startMcpUpstream(opts: UpstreamOptions = {}): Promise<Upstream> {
  const seenHeaders: Record<string, string>[] = [];
  const cancelled: string[] = [];
  const stats = { registrations: 0, authorizeRequests: [] as URLSearchParams[], tokenRequests: [] as URLSearchParams[], refreshes: 0, sessions: 0 };
  let accessTtl = opts.accessTtlSec ?? 3600;
  const clients = new Map<string, { redirectUris: string[] }>();
  const codes = new Map<string, { challenge: string; redirectUri: string; scope: string; clientId: string; resource: string }>();
  const accessTokens = new Set<string>();
  const refreshTokens = new Map<string, string>(); // token -> scope
  let base = "";

  function buildServer(): McpServer {
    const server = new McpServer({ name: "fixture", version: "1.0.0" }, { capabilities: { logging: {} }, instructions: "Use echo for echoing." });
    if (opts.extras) server.registerTool(
      "ask",
      { description: "Asks the user through an elicitation", inputSchema: { delayMs: z.number().optional() } },
      async ({ delayMs }) => {
        if (delayMs) await new Promise((r) => setTimeout(r, delayMs));
        const r = await server.server.elicitInput({
          mode: "form",
          message: "Which colour?",
          requestedSchema: { type: "object", properties: { colour: { type: "string", enum: ["red", "blue"] } }, required: ["colour"] },
        });
        return { content: [{ type: "text", text: `${r.action}:${JSON.stringify(r.content ?? {})}` }] };
      },
    );
    if (opts.extras) server.registerTool("announce", { description: "Announces changed lists", inputSchema: {} }, async () => {
      server.sendToolListChanged();
      server.sendPromptListChanged();
      server.sendResourceListChanged();
      return { content: [{ type: "text", text: "announced" }] };
    });
    server.registerTool("echo", { description: "Echo text back", inputSchema: { text: z.string() } }, async ({ text }) => ({
      content: [{ type: "text", text: `echo: ${text}` }],
    }));
    server.registerTool(
      "slow",
      { description: "Counts with progress", inputSchema: { steps: z.number(), delayMs: z.number() } },
      async ({ steps, delayMs }, extra) => {
        const token = extra._meta?.progressToken;
        for (let i = 1; i <= steps; i++) {
          if (extra.signal.aborted) break;
          await new Promise((r) => setTimeout(r, delayMs));
          if (extra.signal.aborted) break;
          if (token !== undefined) {
            await extra.sendNotification({ method: "notifications/progress", params: { progressToken: token, progress: i, total: steps, message: `step ${i}` } });
          }
        }
        if (extra.signal.aborted) cancelled.push(String(extra.requestId));
        return { content: [{ type: "text", text: `done ${steps}` }] };
      },
    );
    server.registerTool(
      "peek",
      { description: "Read-only tool", inputSchema: {}, annotations: { readOnlyHint: true, destructiveHint: false, title: "Peek" } },
      async () => ({ content: [{ type: "text", text: "peeked" }], isError: false }),
    );
    server.registerTool("fail", { description: "Always fails", inputSchema: {} }, async () => ({ content: [{ type: "text", text: "nope" }], isError: true }));
    server.registerPrompt("greet", { description: "Greeting", argsSchema: { name: z.string() } }, ({ name }) => ({
      messages: [{ role: "user", content: { type: "text", text: `Hello, ${name}!` } }],
    }));
    server.registerResource("hello", "res://hello", { description: "Hello resource", mimeType: "text/plain" }, async (uri) => ({
      contents: [{ uri: uri.href, text: "hello world", mimeType: "text/plain" }],
    }));
    server.registerResource("item", new ResourceTemplate("res://items/{id}", { list: undefined }), { description: "Item by id" }, async (uri, { id }) => ({
      contents: [{ uri: uri.href, text: `item ${id}` }],
    }));
    return server;
  }

  const httpSessions = new Map<string, StreamableHTTPServerTransport>();
  const sseSessions = new Map<string, SSEServerTransport>();

  function authorized(req: IncomingMessage): boolean {
    if (!opts.oauth) return true;
    const m = /^Bearer (.+)$/.exec(req.headers.authorization ?? "");
    return !!m && accessTokens.has(m[1]!);
  }

  function unauthorized(res: ServerResponse) {
    res.writeHead(401, {
      "content-type": "application/json",
      "www-authenticate": `Bearer resource_metadata="${base}/.well-known/oauth-protected-resource/mcp"`,
    });
    res.end(JSON.stringify({ error: "invalid_token" }));
  }

  function issueTokens(scope: string) {
    const access = b64url(randomBytes(24));
    const refresh = b64url(randomBytes(24));
    accessTokens.add(access);
    refreshTokens.set(refresh, scope);
    return { access_token: access, token_type: "Bearer", expires_in: accessTtl, refresh_token: refresh, scope };
  }

  const http = createServer(async (req, res) => {
    try {
      const url = new URL(req.url ?? "/", base);
      const path = url.pathname;

      // --- OAuth -------------------------------------------------------------------------
      if (opts.oauth && path.startsWith("/.well-known/oauth-protected-resource")) {
        return sendJson(res, 200, { resource: `${base}/mcp`, authorization_servers: [base], scopes_supported: ["mcp:read", "mcp:write"] });
      }
      if (opts.oauth && path === "/.well-known/oauth-authorization-server") {
        return sendJson(res, 200, {
          issuer: base,
          authorization_endpoint: `${base}/authorize`,
          token_endpoint: `${base}/token`,
          registration_endpoint: `${base}/register`,
          response_types_supported: ["code"],
          grant_types_supported: ["authorization_code", "refresh_token"],
          code_challenge_methods_supported: ["S256"],
          token_endpoint_auth_methods_supported: ["none"],
          scopes_supported: ["mcp:read", "mcp:write"],
        });
      }
      if (opts.oauth && path === "/register" && req.method === "POST") {
        const body = JSON.parse(await readBody(req));
        const id = `client-${randomUUID()}`;
        clients.set(id, { redirectUris: body.redirect_uris ?? [] });
        stats.registrations++;
        return sendJson(res, 201, { ...body, client_id: id, client_id_issued_at: Math.floor(Date.now() / 1000) });
      }
      if (opts.oauth && path === "/authorize") {
        stats.authorizeRequests.push(url.searchParams);
        const q = url.searchParams;
        const client = clients.get(q.get("client_id") ?? "");
        const redirect = q.get("redirect_uri") ?? "";
        if (!client || !client.redirectUris.includes(redirect)) return sendJson(res, 400, { error: "invalid_client" });
        if (q.get("code_challenge_method") !== "S256" || !q.get("code_challenge")) return sendJson(res, 400, { error: "invalid_request" });
        const loc = new URL(redirect);
        if (q.get("deny") === "1" || url.searchParams.get("scope")?.includes("deny")) {
          loc.searchParams.set("error", "access_denied");
        } else {
          const code = b64url(randomBytes(16));
          codes.set(code, { challenge: q.get("code_challenge")!, redirectUri: redirect, scope: q.get("scope") ?? "", clientId: q.get("client_id")!, resource: q.get("resource") ?? "" });
          loc.searchParams.set("code", code);
        }
        loc.searchParams.set("state", q.get("state") ?? "");
        res.writeHead(302, { location: loc.href });
        return res.end();
      }
      if (opts.oauth && path === "/token" && req.method === "POST") {
        const form = new URLSearchParams(await readBody(req));
        stats.tokenRequests.push(form);
        if (form.get("grant_type") === "authorization_code") {
          const c = codes.get(form.get("code") ?? "");
          codes.delete(form.get("code") ?? "");
          const verifier = form.get("code_verifier") ?? "";
          if (!c || c.redirectUri !== form.get("redirect_uri") || c.clientId !== form.get("client_id")) return sendJson(res, 400, { error: "invalid_grant" });
          if (b64url(createHash("sha256").update(verifier).digest()) !== c.challenge) return sendJson(res, 400, { error: "invalid_grant", error_description: "PKCE" });
          return sendJson(res, 200, issueTokens(c.scope));
        }
        if (form.get("grant_type") === "refresh_token") {
          const scope = refreshTokens.get(form.get("refresh_token") ?? "");
          if (scope === undefined) return sendJson(res, 400, { error: "invalid_grant" });
          refreshTokens.delete(form.get("refresh_token")!);
          stats.refreshes++;
          return sendJson(res, 200, issueTokens(scope));
        }
        return sendJson(res, 400, { error: "unsupported_grant_type" });
      }

      // --- MCP ---------------------------------------------------------------------------
      if (path === "/mcp") {
        seenHeaders.push(Object.fromEntries(Object.entries(req.headers).map(([k, v]) => [k, String(v)])));
        if (!authorized(req)) return unauthorized(res);
        const sid = req.headers["mcp-session-id"] as string | undefined;
        const body = req.method === "POST" ? JSON.parse((await readBody(req)) || "null") : undefined;
        let transport = sid ? httpSessions.get(sid) : undefined;
        if (!transport) {
          if (sid) return sendJson(res, 404, { jsonrpc: "2.0", error: { code: -32001, message: "Session not found" }, id: null });
          transport = new StreamableHTTPServerTransport({
            sessionIdGenerator: () => randomUUID(),
            onsessioninitialized: (id) => {
              httpSessions.set(id, transport!);
              stats.sessions++;
            },
          });
          transport.onclose = () => transport!.sessionId && httpSessions.delete(transport!.sessionId);
          await buildServer().connect(transport);
        }
        return transport.handleRequest(req as IncomingMessage & { auth?: never }, res, body);
      }
      if (path === "/sse" && req.method === "GET") {
        seenHeaders.push(Object.fromEntries(Object.entries(req.headers).map(([k, v]) => [k, String(v)])));
        if (!authorized(req)) return unauthorized(res);
        const t = new SSEServerTransport("/messages", res);
        sseSessions.set(t.sessionId, t);
        res.on("close", () => sseSessions.delete(t.sessionId));
        stats.sessions++;
        await buildServer().connect(t);
        return;
      }
      if (path === "/messages" && req.method === "POST") {
        if (!authorized(req)) return unauthorized(res);
        const t = sseSessions.get(url.searchParams.get("sessionId") ?? "");
        if (!t) return sendJson(res, 404, { error: "unknown session" });
        const body = JSON.parse(await readBody(req));
        return t.handlePostMessage(req, res, body);
      }
      sendJson(res, 404, { error: "not found" });
    } catch (err) {
      if (!res.headersSent) sendJson(res, 500, { error: String(err) });
    }
  });
  await new Promise<void>((r) => http.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(http.address() as AddressInfo).port}`;

  return {
    url: base,
    seenHeaders,
    cancelled,
    stats,
    revokeAccessTokens: () => accessTokens.clear(),
    revokeAll: () => {
      accessTokens.clear();
      refreshTokens.clear();
    },
    setAccessTtl: (sec) => {
      accessTtl = sec;
    },
    close: async () => {
      for (const t of httpSessions.values()) await t.close().catch(() => {});
      for (const t of sseSessions.values()) await t.close().catch(() => {});
      http.closeAllConnections();
      await new Promise<void>((r) => http.close(() => r()));
    },
  };
}
