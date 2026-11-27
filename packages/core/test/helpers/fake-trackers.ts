/**
 * Fakes of Jira (with Confluence and Atlassian's OAuth endpoints at one address) and Linear (GraphQL) for tests: only what the
 * providers rely on, shaped like the fakes in packages/client/tracker/jira and linear.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

function send(res: ServerResponse, status: number, body?: unknown) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(body === undefined ? "" : JSON.stringify(body));
}

async function read(req: IncomingMessage): Promise<string> {
  let s = "";
  for await (const c of req) s += c;
  return s;
}

async function listen(server: Server): Promise<string> {
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  return `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
}

export interface FakeJiraIssue {
  key: string;
  summary: string;
  status: string;
  labels: string[];
}

export class FakeJira {
  url = "";
  email = "alice@example.test";
  apiToken = "jira-token";
  pat = "jira-pat";
  cloudId = "cloud-1";
  issues: FakeJiraIssue[] = [];
  fields: Record<string, Record<string, unknown>> = {};
  comments: { key: string; body: string }[] = [];
  searches: URLSearchParams[] = [];
  tokenRequests: Record<string, string>[] = [];
  private server?: Server;

  private authorized(h: string | undefined): boolean {
    return h === `Basic ${Buffer.from(`${this.email}:${this.apiToken}`).toString("base64")}` || h === `Bearer ${this.pat}` || h === "Bearer atl-access-1" || h === "Bearer atl-access-2";
  }

  private issueJson(i: FakeJiraIssue) {
    return {
      key: i.key,
      fields: {
        summary: i.summary,
        description: `Body of ${i.key}`,
        updated: "2026-01-02T03:04:05.000+0000",
        project: { key: i.key.split("-")[0] },
        status: { name: i.status },
        priority: { name: "High" },
        issuetype: { name: "Task" },
        labels: i.labels,
        comment: { comments: [{ author: { displayName: "Bob" }, body: "first", created: "2026-01-02T03:04:05.000+0000" }] },
      },
    };
  }

  async start(): Promise<void> {
    this.server = createServer(async (req, res) => {
      const u = new URL(req.url!, "http://x");
      const raw = await read(req);
      if (u.pathname === "/oauth/token" && req.method === "POST") {
        const body = JSON.parse(raw) as Record<string, string>;
        this.tokenRequests.push(body);
        if (body.grant_type === "authorization_code" && body.code === "atl-code") return send(res, 200, { access_token: "atl-access-1", refresh_token: "atl-refresh-1", expires_in: 3600 });
        if (body.grant_type === "refresh_token") return send(res, 200, { access_token: "atl-access-2", refresh_token: "atl-refresh-2", expires_in: 3600 });
        return send(res, 400, { error: "invalid_grant" });
      }
      if (!this.authorized(req.headers.authorization)) return send(res, 401, { errorMessages: ["unauthorized"] });
      if (u.pathname === "/oauth/token/accessible-resources") return send(res, 200, [{ id: this.cloudId, url: this.url }]);
      let p = u.pathname.replace(`/ex/jira/${this.cloudId}`, "").replace(`/ex/confluence/${this.cloudId}`, "");
      if (p.startsWith("/wiki/rest/api/")) {
        p = p.slice("/wiki/rest/api/".length);
        const page = (id: string, title: string) => ({ id, title, space: { key: "HELP" }, version: { when: "2026-01-02T03:04:05.000Z" }, body: { storage: { value: `<p>${title}: deploy with care &amp; patience.</p>` } }, _links: { webui: `/spaces/HELP/pages/${id}` }, children: { page: { size: 0 } } });
        if (p === "content/search") return send(res, 200, { results: [page("1", "Deploy guide")], cql: u.searchParams.get("cql") });
        if (p === "content/1") return send(res, 200, page("1", "Deploy guide"));
        return send(res, 404, {});
      }
      p = p.replace(/^\/rest\/api\/2\//, "");
      if (p === "myself") return send(res, 200, { accountId: "acc-alice", name: "alice", emailAddress: this.email, displayName: "Alice" });
      if (p === "search/jql" || p === "search") {
        this.searches.push(u.searchParams);
        const jql = u.searchParams.get("jql") ?? "";
        const m = /status = "([^"]+)"/.exec(jql);
        const list = this.issues.filter((i) => !m || i.status === m[1]);
        return send(res, 200, p === "search" ? { issues: list.map((i) => this.issueJson(i)), total: list.length } : { issues: list.map((i) => this.issueJson(i)), isLast: true });
      }
      if (p === "field") return send(res, 200, [{ id: "customfield_1", name: "Points", custom: true, schema: { type: "number" } }, { id: "labels", name: "Labels", schema: { type: "array", items: "string", system: "labels" } }]);
      if (p === "priority") return send(res, 200, [{ name: "High" }]);
      if (p === "project/search") return send(res, 200, { values: [{ key: "DEMO", name: "Demo" }], isLast: true });
      const im = /^issue\/([^/]+)(\/.*)?$/.exec(p);
      if (im) {
        const issue = this.issues.find((i) => i.key === im[1]);
        if (!issue) return send(res, 404, { errorMessages: ["Issue does not exist"] });
        const rest = im[2] ?? "";
        if (!rest && req.method === "GET") return send(res, 200, this.issueJson(issue));
        if (!rest && req.method === "PUT") {
          const b = JSON.parse(raw) as { fields?: Record<string, unknown>; update?: { labels?: Record<string, string>[] } };
          this.fields[issue.key] = { ...this.fields[issue.key], ...b.fields };
          for (const op of b.update?.labels ?? []) {
            if (op.add) issue.labels.push(op.add);
            if (op.remove) issue.labels = issue.labels.filter((l) => l !== op.remove);
          }
          return send(res, 204);
        }
        if (rest === "/transitions" && req.method === "GET") return send(res, 200, { transitions: [{ id: "21", name: "Review it", to: { name: "In Review" } }, { id: "31", name: "Finish", to: { name: "Done" } }] });
        if (rest === "/transitions") {
          const id = (JSON.parse(raw) as { transition: { id: string } }).transition.id;
          issue.status = id === "21" ? "In Review" : "Done";
          return send(res, 204);
        }
        if (rest === "/comment") {
          this.comments.push({ key: issue.key, body: (JSON.parse(raw) as { body: string }).body });
          return send(res, 201, { id: "c1" });
        }
      }
      send(res, 404, { errorMessages: [`Not found: ${p}`] });
    });
    this.url = await listen(this.server);
  }

  async close(): Promise<void> {
    await new Promise((r) => this.server?.close(r));
  }
}

export interface FakeLinearIssue {
  identifier: string;
  title: string;
  state: string;
}

export class FakeLinear {
  url = "";
  apiKey = "lin_api_good";
  issues: FakeLinearIssue[] = [];
  updates: Record<string, unknown>[] = [];
  comments: Record<string, unknown>[] = [];
  filters: unknown[] = [];
  /** Linear's documents, two to a page. */
  documents: { id: string; title: string; content: string; project: string }[] = [];
  private server?: Server;

  private doc(d: { id: string; title: string; content: string; project: string }) {
    return { id: d.id, title: d.title, content: d.content, url: `https://linear.app/acme/document/${d.id}`, updatedAt: "2026-01-02T03:04:05.000Z", project: d.project ? { name: d.project } : null };
  }

  private node(i: FakeLinearIssue) {
    return {
      id: `uuid-${i.identifier}`,
      identifier: i.identifier,
      title: i.title,
      url: `https://linear.app/acme/issue/${i.identifier}`,
      description: "Body",
      updatedAt: "2026-01-02T03:04:05.000Z",
      priority: 2,
      team: { id: "team-1", key: "ENG" },
      state: { name: i.state },
      assignee: null,
      project: null,
      labels: { nodes: [] },
      comments: { nodes: [{ body: "hi", createdAt: "2026-01-02T03:04:05.000Z", user: { name: "Bob" } }] },
    };
  }

  async start(): Promise<void> {
    this.server = createServer(async (req, res) => {
      const raw = await read(req);
      if (req.url === "/oauth/token") {
        const f = new URLSearchParams(raw);
        if (f.get("code") === "lin-code" || f.get("grant_type") === "refresh_token") return send(res, 200, { access_token: "lin-access", refresh_token: "lin-refresh", expires_in: 86400 });
        return send(res, 400, { error: "invalid_grant" });
      }
      const a = req.headers.authorization;
      if (a !== this.apiKey && a !== "Bearer lin-access") return send(res, 400, { errors: [{ message: "Authentication required, not authenticated", extensions: { code: "AUTHENTICATION_ERROR" } }] });
      const { query: q, variables: v } = JSON.parse(raw) as { query: string; variables: Record<string, any> };
      const data = (d: unknown) => send(res, 200, { data: d });
      if (q.includes("searchDocuments(")) {
        const words = String(v.term).toLowerCase().split(/\s+/);
        return data({ searchDocuments: { nodes: this.documents.filter((d) => words.some((w) => d.content.toLowerCase().includes(w))).map((d) => this.doc(d)) } });
      }
      if (q.includes("documents(")) {
        const name: string | undefined = v.filter?.project?.name?.eqIgnoreCase;
        const list = this.documents.filter((d) => !name || d.project.toLowerCase() === name.toLowerCase());
        const start = v.after ? list.findIndex((d) => d.id === v.after) + 1 : 0;
        const end = Math.min(start + 2, list.length);
        return data({ documents: { nodes: list.slice(start, end).map((d) => this.doc(d)), pageInfo: { hasNextPage: end < list.length, endCursor: list[end - 1]?.id ?? "" } } });
      }
      if (q.includes("document(id")) {
        const d = this.documents.find((x) => x.id === v.id);
        return d ? data({ document: this.doc(d) }) : send(res, 200, { errors: [{ message: "Entity not found: Document" }] });
      }
      if (q.includes("issueUpdate")) {
        this.updates.push(v.in);
        const i = this.issues.find((x) => `uuid-${x.identifier}` === v.id);
        if (i && v.in.stateId) i.state = v.in.stateId === "st-review" ? "In Review" : "Todo";
        return data({ issueUpdate: { success: true } });
      }
      if (q.includes("commentCreate")) {
        this.comments.push(v.in);
        return data({ commentCreate: { success: true } });
      }
      if (q.includes("issues(")) {
        this.filters.push(v.filter);
        const want = JSON.stringify(v.filter).match(/"state":\{"or":\[\{"name":\{"eqIgnoreCase":"([^"]+)"/)?.[1];
        const list = this.issues.filter((i) => !want || i.state.toLowerCase() === want.toLowerCase());
        return data({ issues: { nodes: list.map((i) => this.node(i)), pageInfo: { hasNextPage: false, endCursor: "" } } });
      }
      if (q.includes("issue(id")) {
        const i = this.issues.find((x) => x.identifier === v.id);
        return i ? data({ issue: this.node(i) }) : send(res, 200, { errors: [{ message: "Entity not found: Issue" }] });
      }
      if (q.includes("teams(")) {
        return data({
          teams: {
            nodes: [
              {
                id: "team-1",
                key: "ENG",
                name: "Engineering",
                states: { nodes: [{ id: "st-todo", name: "Todo" }, { id: "st-review", name: "In Review" }] },
                labels: { nodes: [{ id: "lab-bug", name: "Bug" }] },
                members: { nodes: [] },
              },
            ],
          },
        });
      }
      if (q.includes("viewer")) return data({ viewer: { id: "u-alice", name: "Alice", displayName: "alice", email: "alice@example.test" } });
      send(res, 400, { errors: [{ message: "unknown query" }] });
    });
    this.url = await listen(this.server);
  }

  async close(): Promise<void> {
    await new Promise((r) => this.server?.close(r));
  }
}
