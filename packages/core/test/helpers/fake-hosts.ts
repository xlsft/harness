/**
 * Fake code hosts for the VcsService tests, as local HTTP servers:
 * - a GitHub Enterprise Server look-alike (REST under /api/v3, GraphQL at /api/graphql, OAuth at
 *   /login/oauth/*): manifest conversion, app JWT checks (RS256), installations and installation tokens,
 *   user-to-server OAuth with PKCE and refresh tokens, pulls (create / merge / reviews / comments),
 *   GraphQL auto-merge and review threads, check runs + statuses, Actions job logs that redirect to a
 *   separate "blob storage" server (which must not receive credentials);
 * - a GitLab look-alike (REST under /api/v4, OAuth at /oauth/*): OAuth with PKCE, projects, merge requests
 *   (create / merge / merge when pipeline succeeds), approvals, discussions, pipelines, jobs and job traces.
 */
import { createHash, generateKeyPairSync, randomBytes, verify } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export interface Seen {
  method: string;
  path: string;
  auth: string;
  body: string;
}

async function readBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  return Buffer.concat(chunks).toString("utf8");
}

function send(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) {
  res.writeHead(status, { "content-type": "application/json", ...headers });
  res.end(body === undefined ? "" : JSON.stringify(body));
}

function listen(server: Server, host: string): Promise<string> {
  return new Promise((resolve) => server.listen(0, host, () => resolve(`http://${host}:${(server.address() as AddressInfo).port}`)));
}

function close(server: Server): Promise<void> {
  return new Promise((resolve) => {
    server.closeAllConnections();
    server.close(() => resolve());
  });
}

function s256(verifier: string): string {
  return createHash("sha256").update(verifier).digest("base64url");
}

function parseBody(raw: string, contentType: string | undefined): Record<string, any> {
  if (!raw) return {};
  if (contentType?.includes("application/x-www-form-urlencoded")) return Object.fromEntries(new URLSearchParams(raw));
  try {
    return JSON.parse(raw);
  } catch {
    return {};
  }
}

// =====================================================================================================
// GitHub
// =====================================================================================================

export interface FakeGitHub {
  url: string;
  blobUrl: string;
  app: { id: number; slug: string; clientId: string; clientSecret: string; webhookSecret: string; pem: string };
  installationId: number;
  seen: Seen[];
  blobSeen: Seen[];
  /** Issues a one-time manifest code. */
  manifestCode(): string;
  pulls: Map<number, any>;
  checkRuns: Map<string, any[]>;
  statuses: Map<string, any[]>;
  reviewComments: Map<number, any[]>;
  issueComments: Map<number, any[]>;
  graphqlCalls: { query: string; variables: any }[];
  tokenRequests: Record<string, string>[];
  /** expires_in of issued user tokens (seconds); 0 = non-expiring (no refresh token). */
  userTokenTtl: number;
  /** Logins of user tokens that may see acme/widgets. */
  accessLogins: Set<string>;
  addPull(p: Partial<any> & { number: number; head: string; base?: string; sha: string; title?: string }): any;
  close(): Promise<void>;
}

export async function startFakeGitHub(): Promise<FakeGitHub> {
  const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const pem = privateKey.export({ type: "pkcs1", format: "pem" }).toString();
  const seen: Seen[] = [];
  const blobSeen: Seen[] = [];
  const codes = new Set<string>();
  const oauthCodes = new Map<string, { challenge: string; redirectUri: string; login: string }>();
  const userTokens = new Map<string, string>(); // token -> login
  const refreshTokens = new Map<string, string>(); // refresh -> login
  const installTokens = new Set<string>();
  const pulls = new Map<number, any>();
  const checkRuns = new Map<string, any[]>();
  const statuses = new Map<string, any[]>();
  const reviewComments = new Map<number, any[]>();
  const issueComments = new Map<number, any[]>();
  const graphqlCalls: { query: string; variables: any }[] = [];
  const tokenRequests: Record<string, string>[] = [];
  const app = { id: 4242, slug: "harness-test", clientId: "Iv1.testclient", clientSecret: "gh-client-secret", webhookSecret: "gh-webhook-secret", pem };
  const installationId = 77;
  let nextId = 1000;
  let tokenSeq = 0;

  const blob = createServer(async (req, res) => {
    blobSeen.push({ method: req.method!, path: req.url!, auth: req.headers.authorization ?? "", body: "" });
    const lines = Array.from({ length: 4000 }, (_, i) => `line ${i + 1}: ${i === 3999 ? "Error: tests failed" : "ok"}`);
    res.writeHead(200, { "content-type": "text/plain" });
    res.end(lines.join("\n"));
  });
  const blobUrl = await listen(blob, "127.0.0.1");

  let base = "";
  const repoHtml = () => `${base}/acme/widgets`;
  const fake: FakeGitHub = {
    url: "",
    blobUrl,
    app,
    installationId,
    seen,
    blobSeen,
    manifestCode() {
      const c = randomBytes(8).toString("hex");
      codes.add(c);
      return c;
    },
    pulls,
    checkRuns,
    statuses,
    reviewComments,
    issueComments,
    graphqlCalls,
    tokenRequests,
    userTokenTtl: 0,
    accessLogins: new Set(["alice-gh"]),
    addPull(p) {
      const pr = {
        id: nextId++,
        node_id: `PR_node_${p.number}`,
        number: p.number,
        title: p.title ?? `PR ${p.number}`,
        html_url: `${repoHtml()}/pull/${p.number}`,
        state: "open",
        draft: false,
        merged: false,
        merged_at: null,
        mergeable: true,
        mergeable_state: "clean",
        auto_merge: null,
        user: { login: p.author ?? "alice-gh" },
        head: { ref: p.head, sha: p.sha },
        base: { ref: p.base ?? "main", repo: { html_url: repoHtml(), full_name: "acme/widgets" } },
        requested_reviewers: [],
        updated_at: new Date().toISOString(),
        ...p.extra,
      };
      pulls.set(p.number, pr);
      return pr;
    },
    close: async () => {
      await close(server);
      await close(blob);
    },
  };

  function verifyJwt(auth: string): boolean {
    const m = /^Bearer (.+)\.(.+)\.(.+)$/.exec(auth);
    if (!m) return false;
    const ok = verify("RSA-SHA256", Buffer.from(`${m[1]}.${m[2]}`), publicKey, Buffer.from(m[3]!, "base64url"));
    const payload = JSON.parse(Buffer.from(m[2]!, "base64url").toString());
    return ok && String(payload.iss) === String(app.id) && payload.exp > Date.now() / 1000;
  }

  function tokenLogin(auth: string): string | undefined {
    const t = auth.replace(/^Bearer /, "");
    if (installTokens.has(t)) return "app/installation";
    return userTokens.get(t);
  }

  const server = createServer(async (req, res) => {
    const u = new URL(req.url!, base);
    const raw = await readBody(req);
    const auth = req.headers.authorization ?? "";
    seen.push({ method: req.method!, path: u.pathname + u.search, auth, body: raw });
    const body = parseBody(raw, req.headers["content-type"]);
    const p = u.pathname;
    let m: RegExpExecArray | null;

    // --- OAuth (web host) ---
    if (p === "/login/oauth/authorize") {
      const code = randomBytes(8).toString("hex");
      oauthCodes.set(code, { challenge: u.searchParams.get("code_challenge") ?? "", redirectUri: u.searchParams.get("redirect_uri")!, login: "alice-gh" });
      const to = new URL(u.searchParams.get("redirect_uri")!);
      to.searchParams.set("code", code);
      to.searchParams.set("state", u.searchParams.get("state")!);
      res.writeHead(302, { location: to.href });
      return res.end();
    }
    if (p === "/login/oauth/access_token" && req.method === "POST") {
      tokenRequests.push(body);
      if (body.client_id !== app.clientId || body.client_secret !== app.clientSecret) return send(res, 200, { error: "incorrect_client_credentials" });
      let login: string | undefined;
      if (body.grant_type === "refresh_token") {
        login = refreshTokens.get(body.refresh_token);
        refreshTokens.delete(body.refresh_token);
        if (!login) return send(res, 200, { error: "bad_refresh_token" });
      } else {
        const c = oauthCodes.get(body.code);
        oauthCodes.delete(body.code);
        if (!c) return send(res, 200, { error: "bad_verification_code" });
        if (c.challenge && s256(body.code_verifier ?? "") !== c.challenge) return send(res, 200, { error: "invalid_grant", error_description: "PKCE" });
        login = c.login;
      }
      const token = `ghu_${login}_${++tokenSeq}`;
      userTokens.set(token, login);
      const out: Record<string, unknown> = { access_token: token, token_type: "bearer", scope: "" };
      if (fake.userTokenTtl) {
        const refresh = `ghr_${tokenSeq}`;
        refreshTokens.set(refresh, login);
        Object.assign(out, { expires_in: fake.userTokenTtl, refresh_token: refresh, refresh_token_expires_in: 15_000_000 });
      }
      return send(res, 200, out);
    }

    // --- REST ---
    if ((m = /^\/api\/v3\/app-manifests\/([^/]+)\/conversions$/.exec(p)) && req.method === "POST") {
      if (!codes.delete(m[1]!)) return send(res, 404, { message: "Not Found" });
      return send(res, 201, {
        id: app.id,
        slug: app.slug,
        name: "harness test",
        client_id: app.clientId,
        client_secret: app.clientSecret,
        webhook_secret: app.webhookSecret,
        pem: app.pem,
        html_url: `${base}/apps/${app.slug}`,
      });
    }
    if (p === "/api/v3/app") return verifyJwt(auth) ? send(res, 200, { id: app.id, slug: app.slug, name: "harness test" }) : send(res, 401, { message: "Bad credentials" });
    if (p === "/api/v3/app/installations") {
      if (!verifyJwt(auth)) return send(res, 401, { message: "Bad credentials" });
      return send(res, 200, [{ id: installationId, account: { login: "acme", type: "Organization" } }]);
    }
    if ((m = /^\/api\/v3\/app\/installations\/(\d+)\/access_tokens$/.exec(p)) && req.method === "POST") {
      if (!verifyJwt(auth) || Number(m[1]) !== installationId) return send(res, 401, { message: "Bad credentials" });
      const token = `ghs_inst_${++tokenSeq}`;
      installTokens.add(token);
      return send(res, 201, { token, expires_at: new Date(Date.now() + 3_600_000).toISOString() });
    }
    if (p === "/api/v3/installation/repositories") {
      if (!installTokens.has(auth.replace(/^Bearer /, ""))) return send(res, 401, { message: "Bad credentials" });
      return send(res, 200, { total_count: 1, repositories: [{ full_name: "acme/widgets", html_url: repoHtml(), default_branch: "main", private: true }] });
    }
    if (p === "/api/v3/user") {
      const login = tokenLogin(auth);
      return login && login !== "app/installation" ? send(res, 200, { login }) : send(res, 401, { message: "Bad credentials" });
    }
    if (p === "/api/v3/user/repos") {
      const login = tokenLogin(auth);
      if (!login) return send(res, 401, { message: "Bad credentials" });
      return send(res, 200, fake.accessLogins.has(login) ? [{ full_name: "acme/widgets", html_url: repoHtml(), default_branch: "main", private: true }] : []);
    }
    if (p === "/api/v3/repos/acme/widgets/installation") {
      return verifyJwt(auth) ? send(res, 200, { id: installationId, account: { login: "acme", type: "Organization" } }) : send(res, 401, { message: "Bad credentials" });
    }
    if (p === "/api/graphql" && req.method === "POST") {
      if (!tokenLogin(auth)) return send(res, 401, { message: "Bad credentials" });
      graphqlCalls.push({ query: body.query, variables: body.variables });
      const pr = [...pulls.values()].find((x) => x.node_id === body.variables?.id);
      if (String(body.query).includes("enablePullRequestAutoMerge")) {
        if (!pr) return send(res, 200, { errors: [{ message: "not found" }] });
        pr.auto_merge = { merge_method: String(body.variables.method).toLowerCase() };
        return send(res, 200, { data: { enablePullRequestAutoMerge: { clientMutationId: null } } });
      }
      if (String(body.query).includes("disablePullRequestAutoMerge")) {
        if (pr) pr.auto_merge = null;
        return send(res, 200, { data: { disablePullRequestAutoMerge: { clientMutationId: null } } });
      }
      if (String(body.query).includes("reviewThreads")) {
        const n = body.variables.number;
        const resolved = (reviewComments.get(n) ?? []).filter((c) => c.resolved);
        return send(res, 200, { data: { repository: { pullRequest: { reviewThreads: { nodes: resolved.map((c) => ({ isResolved: true, comments: { nodes: [{ databaseId: c.id }] } })) } } } } });
      }
      return send(res, 200, { errors: [{ message: "unknown query" }] });
    }

    const repo = /^\/api\/v3\/repos\/acme\/widgets(\/.*)?$/.exec(p);
    if (repo) {
      const who = tokenLogin(auth);
      if (!who || (who !== "app/installation" && !fake.accessLogins.has(who))) return send(res, 404, { message: "Not Found" });
      const rest = repo[1] ?? "";
      if (rest === "") return send(res, 200, { full_name: "acme/widgets", html_url: repoHtml(), default_branch: "main", private: true });
      if (rest === "/pulls" && req.method === "GET") {
        const state = u.searchParams.get("state") ?? "open";
        const head = u.searchParams.get("head");
        const list = [...pulls.values()].filter((x) => (state === "all" || x.state === state) && (!head || `acme:${x.head.ref}` === head));
        return send(res, 200, list);
      }
      if (rest === "/pulls" && req.method === "POST") {
        if (who === "app/installation") return send(res, 403, { message: "Resource not accessible by integration" });
        const n = Math.max(0, ...pulls.keys()) + 1;
        const pr = fake.addPull({ number: n, head: body.head, base: body.base, sha: `sha-${n}`, title: body.title, author: who, extra: { draft: !!body.draft, body: body.body } });
        return send(res, 201, pr);
      }
      if ((m = /^\/pulls\/(\d+)$/.exec(rest))) {
        const pr = pulls.get(Number(m[1]));
        return pr ? send(res, 200, pr) : send(res, 404, { message: "Not Found" });
      }
      if ((m = /^\/pulls\/(\d+)\/merge$/.exec(rest)) && req.method === "PUT") {
        const pr = pulls.get(Number(m[1]));
        if (!pr) return send(res, 404, { message: "Not Found" });
        if (pr.mergeable === false) return send(res, 405, { message: "Pull Request is not mergeable" });
        Object.assign(pr, { merged: true, merged_at: new Date().toISOString(), state: "closed", merge_method_used: body.merge_method, merged_by: who });
        return send(res, 200, { merged: true, sha: "merge-sha" });
      }
      if ((m = /^\/pulls\/(\d+)\/reviews$/.exec(rest))) return send(res, 200, pulls.get(Number(m[1]))?.reviews ?? []);
      if ((m = /^\/pulls\/(\d+)\/comments$/.exec(rest))) return send(res, 200, reviewComments.get(Number(m[1])) ?? []);
      if ((m = /^\/pulls\/(\d+)\/comments\/(\d+)\/replies$/.exec(rest)) && req.method === "POST") {
        const n = Number(m[1]);
        const c = { id: nextId++, user: { login: who }, body: body.body, path: "src/a.ts", line: 3, in_reply_to_id: Number(m[2]), html_url: `${repoHtml()}/pull/${n}#r`, created_at: new Date().toISOString() };
        reviewComments.set(n, [...(reviewComments.get(n) ?? []), c]);
        return send(res, 201, c);
      }
      if ((m = /^\/issues\/(\d+)\/comments$/.exec(rest))) {
        const n = Number(m[1]);
        if (req.method === "POST") {
          const c = { id: nextId++, user: { login: who }, body: body.body, html_url: `${repoHtml()}/pull/${n}#c`, created_at: new Date().toISOString() };
          issueComments.set(n, [...(issueComments.get(n) ?? []), c]);
          return send(res, 201, c);
        }
        return send(res, 200, issueComments.get(n) ?? []);
      }
      if ((m = /^\/commits\/([^/]+)\/check-runs$/.exec(rest))) {
        const runs = checkRuns.get(m[1]!) ?? [];
        return send(res, 200, { total_count: runs.length, check_runs: runs });
      }
      if ((m = /^\/commits\/([^/]+)\/status$/.exec(rest))) return send(res, 200, { state: "pending", statuses: statuses.get(m[1]!) ?? [] });
      if ((m = /^\/actions\/jobs\/(\d+)\/logs$/.exec(rest))) {
        res.writeHead(302, { location: `${blobUrl}/logs/${m[1]}?sig=signed` });
        return res.end();
      }
    }
    send(res, 404, { message: `Not Found: ${req.method} ${p}` });
  });
  base = await listen(server, "127.0.0.1");
  fake.url = base;
  return fake;
}

// =====================================================================================================
// GitLab
// =====================================================================================================

export interface FakeGitLab {
  url: string;
  clientId: string;
  clientSecret: string;
  accessToken: string;
  seen: Seen[];
  tokenRequests: Record<string, string>[];
  mrs: Map<number, any>;
  jobs: Map<number, any[]>;
  discussions: Map<number, any[]>;
  addMr(m: { iid: number; source: string; target?: string; sha: string; title?: string; pipelineId?: number }): any;
  close(): Promise<void>;
}

export async function startFakeGitLab(host = "localhost"): Promise<FakeGitLab> {
  const seen: Seen[] = [];
  const tokenRequests: Record<string, string>[] = [];
  const codes = new Map<string, { challenge: string }>();
  const tokens = new Map<string, string>(); // token -> username
  const refreshes = new Map<string, string>();
  const mrs = new Map<number, any>();
  const jobs = new Map<number, any[]>();
  const discussions = new Map<number, any[]>();
  const clientId = "gl-app-id";
  const clientSecret = "gl-app-secret";
  const accessToken = "glpat-group-token";
  tokens.set(accessToken, "group_bot");
  let seq = 0;
  let base = "";
  const web = () => `${base}/acme/sub/app`;

  const fake: FakeGitLab = {
    url: "",
    clientId,
    clientSecret,
    accessToken,
    seen,
    tokenRequests,
    mrs,
    jobs,
    discussions,
    addMr(m) {
      const mr = {
        id: 5000 + m.iid,
        iid: m.iid,
        title: m.title ?? `MR ${m.iid}`,
        web_url: `${web()}/-/merge_requests/${m.iid}`,
        source_branch: m.source,
        target_branch: m.target ?? "main",
        author: { username: "alice-gl" },
        state: "opened",
        draft: false,
        sha: m.sha,
        detailed_merge_status: "mergeable",
        merge_status: "can_be_merged",
        has_conflicts: false,
        merge_when_pipeline_succeeds: false,
        head_pipeline: m.pipelineId ? { id: m.pipelineId } : null,
        updated_at: new Date().toISOString(),
      };
      mrs.set(m.iid, mr);
      return mr;
    },
    close: () => close(server),
  };

  const server = createServer(async (req, res) => {
    const u = new URL(req.url!, base);
    const raw = await readBody(req);
    const auth = req.headers.authorization ?? "";
    seen.push({ method: req.method!, path: u.pathname + u.search, auth, body: raw });
    const body = parseBody(raw, req.headers["content-type"]);
    const p = u.pathname;
    let m: RegExpExecArray | null;

    if (p === "/.well-known/openid-configuration") return send(res, 200, { issuer: base });
    if (p === "/oauth/authorize") {
      if (u.searchParams.get("client_id") !== clientId || u.searchParams.get("response_type") !== "code") return send(res, 400, { error: "invalid_request" });
      const code = randomBytes(8).toString("hex");
      codes.set(code, { challenge: u.searchParams.get("code_challenge") ?? "" });
      const to = new URL(u.searchParams.get("redirect_uri")!);
      to.searchParams.set("code", code);
      to.searchParams.set("state", u.searchParams.get("state")!);
      res.writeHead(302, { location: to.href });
      return res.end();
    }
    if (p === "/oauth/token" && req.method === "POST") {
      tokenRequests.push(body);
      if (body.client_id !== clientId || body.client_secret !== clientSecret) return send(res, 401, { error: "invalid_client" });
      let user: string | undefined;
      if (body.grant_type === "refresh_token") {
        user = refreshes.get(body.refresh_token);
        refreshes.delete(body.refresh_token);
      } else {
        const c = codes.get(body.code);
        codes.delete(body.code);
        if (!c || s256(body.code_verifier ?? "") !== c.challenge) return send(res, 400, { error: "invalid_grant", error_description: "PKCE verification failed" });
        user = "alice-gl";
      }
      if (!user) return send(res, 400, { error: "invalid_grant" });
      const token = `glu_${++seq}`;
      const refresh = `glr_${seq}`;
      tokens.set(token, user);
      refreshes.set(refresh, user);
      return send(res, 200, { access_token: token, refresh_token: refresh, token_type: "Bearer", expires_in: 7200, created_at: Math.floor(Date.now() / 1000) });
    }
    if (p === "/oauth/revoke") return send(res, 200, {});

    const who = tokens.get(auth.replace(/^Bearer /, ""));
    if (!p.startsWith("/api/v4/")) return send(res, 404, { message: "404 Not Found" });
    if (!who) return send(res, 401, { message: "401 Unauthorized" });
    if (p === "/api/v4/user") return send(res, 200, { username: who });
    if (p === "/api/v4/projects") {
      return send(res, 200, [{ path_with_namespace: "acme/sub/app", web_url: web(), default_branch: "main", visibility: "private" }], { "x-next-page": "" });
    }
    const proj = /^\/api\/v4\/projects\/acme%2Fsub%2Fapp(\/.*)?$/i.exec(req.url!.split("?")[0]!);
    if (!proj) return send(res, 404, { message: "404 Project Not Found" });
    const rest = proj[1] ?? "";
    if (rest === "") return send(res, 200, { path_with_namespace: "acme/sub/app", web_url: web() });
    if (rest === "/merge_requests" && req.method === "GET") {
      const state = u.searchParams.get("state") ?? "opened";
      const src = u.searchParams.get("source_branch");
      return send(res, 200, [...mrs.values()].filter((x) => (state === "all" || x.state === state) && (!src || x.source_branch === src)));
    }
    if (rest === "/merge_requests" && req.method === "POST") {
      const iid = Math.max(0, ...mrs.keys()) + 1;
      const mr = fake.addMr({ iid, source: body.source_branch, target: body.target_branch, sha: `glsha-${iid}`, title: body.title });
      mr.author = { username: who };
      mr.draft = /^draft:/i.test(body.title);
      return send(res, 201, mr);
    }
    if ((m = /^\/merge_requests\/(\d+)$/.exec(rest))) {
      const mr = mrs.get(Number(m[1]));
      return mr ? send(res, 200, mr) : send(res, 404, { message: "404 Not found" });
    }
    if ((m = /^\/merge_requests\/(\d+)\/merge$/.exec(rest)) && req.method === "PUT") {
      const mr = mrs.get(Number(m[1]));
      if (!mr) return send(res, 404, { message: "404 Not found" });
      if (body.merge_when_pipeline_succeeds) {
        mr.merge_when_pipeline_succeeds = true;
        mr.squash = !!body.squash;
        return send(res, 200, mr);
      }
      Object.assign(mr, { state: "merged", squash: !!body.squash, merged_by: { username: who } });
      return send(res, 200, mr);
    }
    if ((m = /^\/merge_requests\/(\d+)\/cancel_merge_when_pipeline_succeeds$/.exec(rest)) && req.method === "POST") {
      const mr = mrs.get(Number(m[1]));
      if (mr) mr.merge_when_pipeline_succeeds = false;
      return send(res, 201, mr);
    }
    if ((m = /^\/merge_requests\/(\d+)\/approvals$/.exec(rest))) return send(res, 200, { approved: false, approvals_left: 1, approved_by: [] });
    if ((m = /^\/merge_requests\/(\d+)\/discussions$/.exec(rest))) {
      const n = Number(m[1]);
      if (req.method === "POST") {
        const d = { id: randomBytes(20).toString("hex"), notes: [{ id: ++seq, body: body.body, author: { username: who }, system: false, resolvable: false, created_at: new Date().toISOString() }] };
        discussions.set(n, [...(discussions.get(n) ?? []), d]);
        return send(res, 201, d);
      }
      return send(res, 200, discussions.get(n) ?? []);
    }
    if ((m = /^\/merge_requests\/(\d+)\/discussions\/([0-9a-f]+)\/notes$/.exec(rest)) && req.method === "POST") {
      const d = (discussions.get(Number(m[1])) ?? []).find((x) => x.id === m![2]);
      if (!d) return send(res, 404, { message: "404 Discussion Not Found" });
      const note = { id: ++seq, body: body.body, author: { username: who }, system: false, resolvable: true, resolved: false, created_at: new Date().toISOString() };
      d.notes.push(note);
      return send(res, 201, note);
    }
    if ((m = /^\/merge_requests\/(\d+)\/pipelines$/.exec(rest))) return send(res, 200, []);
    if (rest === "/pipelines") return send(res, 200, [...jobs.keys()].map((id) => ({ id })).slice(0, 1));
    if ((m = /^\/pipelines\/(\d+)\/jobs$/.exec(rest))) return send(res, 200, jobs.get(Number(m[1])) ?? []);
    if ((m = /^\/jobs\/(\d+)\/trace$/.exec(rest))) {
      res.writeHead(200, { "content-type": "text/plain" });
      return res.end(`${"x".repeat(10_000)}\nJob ${m[1]} failed: exit code 1\n`);
    }
    send(res, 404, { message: `404 ${req.method} ${p}` });
  });
  base = await listen(server, host);
  fake.url = base;
  return fake;
}
