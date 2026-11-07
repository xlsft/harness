/** A fake YouTrack and Hub for tests: only what the provider relies on, shaped like packages/client/tracker/youtrack/youtracktest. */
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export interface FakeIssue {
  id: string;
  project: string;
  summary: string;
  description?: string;
  stage: string;
  updated?: number;
}

export interface FakeArticle {
  id: string;
  project: string;
  summary: string;
  content: string;
  parent?: string;
}

const STAGE_FIELD = {
  $type: "StateProjectCustomField",
  field: { name: "Stage", fieldType: { id: "state[1]", isMultiValue: false, valueType: "state" } },
  bundle: { values: [{ name: "Backlog" }, { name: "Develop" }, { name: "Review" }, { name: "Old", archived: true }] },
};

export class FakeYouTrack {
  url = "";
  token = "perm:good";
  login = "alice";
  password = "s3cret";
  issues: FakeIssue[] = [];
  articles: FakeArticle[] = [];
  comments: { issue: string; text: string; deleted?: boolean }[] = [];
  /** The next answers, in order, before anything else: status codes. */
  failures: number[] = [];
  /** Project settings say 403 as they do for a regular user. */
  regularUser = false;
  /** Ignore the query on /api/articles, as an instance might. */
  ignoreArticleQuery = false;
  /** Every issue search query seen. */
  searches: string[] = [];
  hubTokens = new Set<string>();
  oauthCodes = new Map<string, string>();
  refreshTokens = new Set<string>();
  private server = createServer((req, res) => void this.handle(req, res));
  private seq = 0;

  async start(): Promise<void> {
    await new Promise<void>((r) => this.server.listen(0, "127.0.0.1", r));
    this.url = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
  }

  async close(): Promise<void> {
    await new Promise((r) => this.server.close(r));
  }

  private json(res: ServerResponse, status: number, body: unknown) {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(body));
  }

  private toIssue(i: FakeIssue, full: boolean) {
    return {
      idReadable: i.id,
      summary: i.summary,
      updated: i.updated ?? 1,
      project: { id: `p-${i.project}`, shortName: i.project, name: i.project },
      ...(full ? { description: i.description ?? "", customFields: [{ name: "Stage", value: { name: i.stage } }] } : {}),
      customFieldsSchema: undefined,
    };
  }

  private async handle(req: IncomingMessage, res: ServerResponse) {
    const u = new URL(req.url ?? "/", this.url);
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    const body = Buffer.concat(chunks).toString();

    if (u.pathname === "/api/config") return this.json(res, 200, { ring: { url: "/hub", serviceId: "svc-1" } });
    if (u.pathname === "/hub/api/rest/oauth2/interactive/login") {
      const form = new URLSearchParams(body);
      if (form.get("username") !== this.login || form.get("password") !== this.password) {
        res.writeHead(303, { location: "/hub/auth/login?message=bad" });
        return res.end();
      }
      u.searchParams.set("request_credentials", "skip");
      res.writeHead(303, { location: `/hub/api/rest/oauth2/auth?${u.searchParams}`, "set-cookie": "JSESSIONID=sid; Path=/hub" });
      return res.end();
    }
    if (u.pathname === "/hub/api/rest/oauth2/auth") {
      const frag = new URLSearchParams({ state: u.searchParams.get("state") ?? "" });
      if ((req.headers.cookie ?? "").includes("JSESSIONID=sid")) {
        const t = `hub-${++this.seq}`;
        this.hubTokens.add(t);
        frag.set("access_token", t);
        frag.set("expires_in", "3600");
      } else {
        frag.set("error", "access_denied");
      }
      res.writeHead(302, { location: `${u.searchParams.get("redirect_uri")}#${frag}` });
      return res.end();
    }
    if (u.pathname === "/hub/api/rest/oauth2/token") {
      const form = new URLSearchParams(body);
      const grant = form.get("grant_type");
      if (grant === "authorization_code" && this.oauthCodes.get(form.get("code") ?? "") !== undefined) {
        const t = `oauth-${++this.seq}`;
        this.hubTokens.add(t);
        this.refreshTokens.add("r1");
        return this.json(res, 200, { access_token: t, refresh_token: "r1", expires_in: 3600 });
      }
      if (grant === "refresh_token" && this.refreshTokens.has(form.get("refresh_token") ?? "")) {
        const t = `oauth-${++this.seq}`;
        this.hubTokens.add(t);
        return this.json(res, 200, { access_token: t, expires_in: 3600 });
      }
      return this.json(res, 400, { error: "invalid_grant" });
    }

    const auth = (req.headers.authorization ?? "").replace(/^Bearer /, "");
    if (auth !== this.token && !this.hubTokens.has(auth)) return this.json(res, 401, { error: "Unauthorized", error_description: "Invalid token" });
    const fail = this.failures.shift();
    if (fail) return this.json(res, fail, { error: "fail", error_description: `Failing with ${fail}` });

    const path = u.pathname.replace(/^\/api\//, "");
    if (path === "users/me") return this.json(res, 200, { login: "alice", fullName: "Alice A" });
    if (path === "admin/projects") {
      const keys = [...new Set(this.issues.map((i) => i.project))];
      return this.json(res, 200, keys.map((k) => ({ id: `p-${k}`, shortName: k, name: `Project ${k}`, description: `About ${k}`, iconUrl: `/icons/${k}.png` })));
    }
    if (/^admin\/projects\/[^/]+\/customFields$/.test(path)) {
      if (this.regularUser) return this.json(res, 403, { error: "Forbidden" });
      return this.json(res, 200, [STAGE_FIELD]);
    }
    if (path === "issues" && req.method === "GET") {
      const q = u.searchParams.get("query") ?? "";
      this.searches.push(q);
      const wantedStage = /Stage: *(\w+)/.exec(q)?.[1];
      const wantedProject = /project: *\{?(\w+)\}?/.exec(q)?.[1];
      const list = this.issues.filter((i) => (!wantedStage || i.stage === wantedStage) && (!wantedProject || i.project === wantedProject));
      const top = Number(u.searchParams.get("$top") ?? 100);
      if ((u.searchParams.get("fields") ?? "").includes("projectCustomField")) {
        return this.json(res, 200, list.slice(0, top).map(() => ({ customFields: [{ projectCustomField: STAGE_FIELD }] })));
      }
      return this.json(res, 200, list.slice(0, top).map((i) => this.toIssue(i, false)));
    }
    const m = /^issues\/([^/]+)(\/comments)?$/.exec(path);
    if (m) {
      const issue = this.issues.find((i) => i.id === decodeURIComponent(m[1]!));
      if (!issue) return this.json(res, 404, { error: "Not Found", error_description: "Entity with id not found" });
      if (m[2]) {
        if (req.method === "POST") {
          this.comments.push({ issue: issue.id, text: (JSON.parse(body) as { text: string }).text });
          return this.json(res, 200, { id: "c" });
        }
        return this.json(res, 200, this.comments.filter((c) => c.issue === issue.id).map((c) => ({ text: c.deleted ? null : c.text, created: 1, deleted: !!c.deleted, author: { login: "alice" } })));
      }
      if (req.method === "POST") {
        const b = JSON.parse(body) as { summary?: string; customFields?: { name: string; value: { name?: string } }[] };
        if (b.summary) issue.summary = b.summary;
        for (const f of b.customFields ?? []) if (f.name === "Stage" && f.value.name) issue.stage = f.value.name;
        return this.json(res, 200, { idReadable: issue.id });
      }
      return this.json(res, 200, this.toIssue(issue, true));
    }
    if (path === "articles") {
      const q = this.ignoreArticleQuery ? "" : (u.searchParams.get("query") ?? "");
      const words = q.split(/\s+/).filter((w) => w && !w.includes(":"));
      const list = this.articles.filter((a) => words.every((w) => `${a.summary} ${a.content}`.toLowerCase().includes(w.toLowerCase())));
      return this.json(res, 200, list.map((a) => this.toArticle(a)));
    }
    const am = /^articles\/([^/]+)(\/childArticles)?$/.exec(path);
    if (am) {
      const a = this.articles.find((x) => x.id === decodeURIComponent(am[1]!));
      if (!a) return this.json(res, 404, { error: "Not Found" });
      if (am[2]) return this.json(res, 200, this.articles.filter((x) => x.parent === a.id).map((x) => this.toArticle(x)));
      return this.json(res, 200, this.toArticle(a));
    }
    return this.json(res, 404, { error: "Not Found", error_description: path });
  }

  private toArticle(a: FakeArticle) {
    return {
      idReadable: a.id,
      summary: a.summary,
      content: a.content,
      updated: 1000,
      project: { shortName: a.project },
      parentArticle: a.parent ? { idReadable: a.parent } : null,
      childArticles: this.articles.filter((x) => x.parent === a.id).map((x) => ({ idReadable: x.id })),
    };
  }
}
