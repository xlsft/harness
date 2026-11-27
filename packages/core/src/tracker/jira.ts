/**
 * Jira, Cloud and Data Center, for company connections: what packages/client/tracker/jira does on the client, for requests that
 * must come from the server. Confluence, the knowledge provider that borrows a Jira sign-in, is src/knowledge/confluence.ts and
 * uses the article methods here. docs/design/tracker.md, "Writing a provider".
 *
 * Sign-in, checked against Atlassian's documentation:
 *   - Cloud: account email and API token as basic auth (https://developer.atlassian.com/cloud/jira/platform/basic-auth-for-rest-apis/)
 *   - Data Center: personal access token as a bearer token (https://confluence.atlassian.com/enterprise/using-personal-access-tokens-1026032365.html)
 *   - OAuth 2.0 (3LO) with PKCE on Cloud (https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/), and Data Center's
 *     own provider (https://confluence.atlassian.com/adminjiraserver/jira-oauth-2-0-provider-api-1115659070.html)
 */
import { JsonClient, oauthTokens, splitList, TokenSource, htmlText, type OAuthTokens } from "./http.ts";
import { TrackerError, type Article, type AuthHint, type AuthMethodName, type Capabilities, type Field, type OAuthModule, type OpenOptions, type TrackerModule, type Identity, type Issue, type IssueDraft, type IssueUpdate, type Page, type ProviderProject, type TrackerProvider } from "./provider.ts";

export const ATLASSIAN_AUTH_HINTS: Partial<Record<AuthMethodName, AuthHint>> = {
  email_token: { help: "Jira Cloud: your Atlassian account email and an API token from id.atlassian.com → Security → API tokens.", secretLabel: "API token", loginLabel: "Account email" },
  token: { help: "Jira Data Center: a personal access token from your profile → Personal Access Tokens.", secretLabel: "Personal access token" },
  oauth: { help: "An OAuth 2.0 app: on Cloud from developer.atlassian.com (3LO), on Data Center an incoming link with OAuth 2.0." },
};

export const JIRA_CAPABILITIES: Capabilities = {
  provider: "jira",
  displayName: "Jira",
  authMethods: ["email_token", "token", "oauth"],
  queryLanguage: "JQL",
  queryExample: 'project = DEMO AND status = "To Do"',
  customFields: true,
  comments: true,
  docsUrl: "https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/",
  logo: "jira",
  baseUrlPlaceholder: "https://your-site.atlassian.net",
  authHints: ATLASSIAN_AUTH_HINTS,
};

/** Atlassian's OAuth 2.0 (3LO) hosts for Cloud, and what counts as Cloud. Tests point them at a fake. */
export const atlassian = {
  authUrl: "https://auth.atlassian.com",
  gatewayUrl: "https://api.atlassian.com",
  isCloud: (u: URL): boolean => /\.(atlassian\.net|jira\.com)$/i.test(u.hostname),
};

export const JIRA_OAUTH_SCOPES = "read:jira-work write:jira-work read:jira-user read:confluence-content.all read:confluence-space.summary search:confluence offline_access";

const FIELDS_TTL_MS = 5 * 60_000;

export interface JiraOptions {
  baseUrl: string;
  auth: "email_token" | "token" | "oauth";
  login?: string;
  secret?: string;
  oauth?: { clientId: string; clientSecret?: string; accessToken?: string; refreshToken?: string; expiresAt?: number };
  onOAuthTokens?: (t: OAuthTokens) => Promise<void>;
  knowledgeUrl?: string;
  allowPrivateNetwork: boolean;
  retryDelaysMs?: number[];
}

const trimUrl = (s: string) => s.trim().replace(/\/+$/, "");

/** The authorize URL and token endpoint of a site: Atlassian's for Cloud, the instance's own on Data Center. */
export function jiraOAuth(baseUrl: string) {
  const site = trimUrl(baseUrl);
  const cloud = atlassian.isCloud(new URL(site));
  return {
    cloud,
    tokenUrl: cloud ? `${atlassian.authUrl}/oauth/token` : `${site}/rest/oauth2/latest/token`,
    authorizeUrl(p: { clientId: string; redirectUri: string; state: string; challenge: string }): string {
      const q = new URLSearchParams({ client_id: p.clientId, redirect_uri: p.redirectUri, state: p.state, response_type: "code", code_challenge: p.challenge, code_challenge_method: "S256" });
      if (!cloud) {
        q.set("scope", "WRITE");
        return `${site}/rest/oauth2/latest/authorize?${q}`;
      }
      q.set("audience", "api.atlassian.com");
      q.set("scope", JIRA_OAUTH_SCOPES);
      q.set("prompt", "consent");
      return `${atlassian.authUrl}/authorize?${q}`;
    },
  };
}

interface ApiUser {
  accountId?: string;
  name?: string;
  emailAddress?: string;
  displayName?: string;
}

interface ApiIssue {
  key: string;
  fields: {
    summary?: string;
    description?: unknown;
    updated?: string;
    project?: { key?: string };
    status?: { name?: string };
    priority?: { name?: string };
    issuetype?: { name?: string };
    assignee?: ApiUser | null;
    labels?: string[];
    comment?: { comments?: { author?: ApiUser; body?: unknown; created?: string }[] };
  };
}

interface ApiField {
  id: string;
  name: string;
  custom?: boolean;
  schema?: { type?: string; items?: string; system?: string };
}

interface ApiPage {
  id: string;
  title: string;
  space?: { key?: string };
  version?: { when?: string };
  body?: { storage?: { value?: string } };
  ancestors?: { id: string }[];
  children?: { page?: { size?: number } };
  _links?: { webui?: string; base?: string };
}

/** Jira's 2024-01-02T03:04:05.000+0000 as RFC 3339 without milliseconds, as the Go side gives it. */
export function jiraTime(s: string | undefined): string {
  if (!s) return "";
  const t = Date.parse(s.replace(/([+-]\d\d)(\d\d)$/, "$1:$2"));
  return Number.isNaN(t) ? s : new Date(t).toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** A v2 string body, or the text of a v3 document. */
function text(v: unknown): string {
  if (typeof v === "string") return v;
  if (!v || typeof v !== "object") return "";
  const d = v as { text?: string; type?: string; content?: unknown[] };
  let out = d.text ?? "";
  for (const c of d.content ?? []) out += text(c);
  if (d.type === "paragraph" || d.type === "heading") out += "\n";
  return out;
}

export function looksLikeCql(q: string): boolean {
  const low = ` ${q.toLowerCase()} `;
  return ["~", "=", " and ", " or ", " order by ", " in ("].some((op) => low.includes(op));
}

/** The search a Knowledge query becomes: pages, narrowed by base, matching the words or the query as CQL. */
export function cql(query: string, base: string): string {
  const parts = ["type = page"];
  if (base.trim()) parts.push(`(${base.trim()})`);
  const q = query.trim();
  if (q) parts.push(looksLikeCql(q) ? `(${q})` : `text ~ "${q.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`);
  return parts.join(" AND ");
}

const isStatus = (n: string) => ["status", "state", "stage"].includes(n.toLowerCase());

export class Jira implements TrackerProvider {
  private readonly site: string;
  private readonly cloud: boolean;
  private cloudId = "";
  private fieldList?: { list: ApiField[]; at: number };
  private readonly tokens?: TokenSource;

  constructor(private readonly o: JiraOptions) {
    this.site = trimUrl(o.baseUrl);
    this.cloud = atlassian.isCloud(new URL(this.site));
    if (o.auth === "oauth") {
      const a = o.oauth ?? { clientId: "" };
      const ep = jiraOAuth(this.site);
      this.tokens = new TokenSource(
        { accessToken: a.accessToken, refreshToken: a.refreshToken, expiresAt: a.expiresAt },
        (refresh) =>
          oauthTokens(ep.tokenUrl, { grant_type: "refresh_token", client_id: a.clientId, refresh_token: refresh, ...(a.clientSecret ? { client_secret: a.clientSecret } : {}) }, { asJson: ep.cloud, allowPrivateNetwork: o.allowPrivateNetwork, name: "Atlassian" }),
        o.onOAuthTokens,
      );
    }
  }

  capabilities(): Capabilities {
    return JIRA_CAPABILITIES;
  }

  private async authorize(): Promise<Record<string, string>> {
    switch (this.o.auth) {
      case "email_token":
        if (!this.o.secret) throw new TrackerError("auth", "No API token is stored for this connection");
        return { authorization: `Basic ${Buffer.from(`${this.o.login ?? ""}:${this.o.secret}`).toString("base64")}` };
      case "token":
        if (!this.o.secret) throw new TrackerError("auth", "No personal access token is stored for this connection");
        return { authorization: `Bearer ${this.o.secret}` };
      case "oauth":
        return { authorization: `Bearer ${await this.tokens!.token()}` };
    }
  }

  private client(base: string, name: string): JsonClient {
    return new JsonClient({ base, name, allowPrivateNetwork: this.o.allowPrivateNetwork, authorize: () => this.authorize(), retryDelaysMs: this.o.retryDelaysMs });
  }

  private gateway(): boolean {
    return this.cloud && this.o.auth === "oauth";
  }

  private async resolveCloudId(): Promise<string> {
    if (this.cloudId) return this.cloudId;
    const sites = await this.client(atlassian.gatewayUrl, "Atlassian").get<{ id: string; url: string }[]>("oauth/token/accessible-resources");
    const hit = (sites ?? []).find((s) => trimUrl(s.url).toLowerCase() === this.site.toLowerCase());
    if (!hit) throw new TrackerError("forbidden", `The authorization does not cover ${this.site}`);
    this.cloudId = hit.id;
    return hit.id;
  }

  private async jira(): Promise<JsonClient> {
    if (this.gateway()) return this.client(`${atlassian.gatewayUrl}/ex/jira/${await this.resolveCloudId()}/rest/api/2`, "Jira");
    return this.client(`${this.site}/rest/api/2`, "Jira");
  }

  private async wiki(): Promise<{ c: JsonClient; web: string }> {
    const k = this.o.knowledgeUrl ? trimUrl(this.o.knowledgeUrl) : "";
    if (k) return { c: this.client(`${k}/rest/api`, "Confluence"), web: k };
    if (this.gateway()) return { c: this.client(`${atlassian.gatewayUrl}/ex/confluence/${await this.resolveCloudId()}/wiki/rest/api`, "Confluence"), web: `${this.site}/wiki` };
    if (this.cloud) return { c: this.client(`${this.site}/wiki/rest/api`, "Confluence"), web: `${this.site}/wiki` };
    throw new TrackerError("other", "Set the Confluence address of this connection: on Data Center it has its own");
  }

  async test(): Promise<Identity> {
    const me = await (await this.jira()).get<ApiUser>("myself");
    return { login: me.emailAddress || me.name || me.accountId || "", displayName: me.displayName ?? "" };
  }

  private issue(i: ApiIssue): Issue {
    const f = i.fields ?? {};
    const fields: { name: string; value: string }[] = [];
    const add = (name: string, v: string | undefined) => v && fields.push({ name, value: v });
    add("Status", f.status?.name);
    add("Type", f.issuetype?.name);
    add("Priority", f.priority?.name);
    add("Assignee", f.assignee?.displayName);
    add("Labels", (f.labels ?? []).join(", "));
    const comments = (f.comment?.comments ?? []).map((c) => ({ author: c.author?.displayName ?? "", text: text(c.body).trim(), createdAt: jiraTime(c.created) })).slice(-10);
    return { id: i.key, title: f.summary ?? "", url: `${this.site}/browse/${i.key}`, description: text(f.description).trim(), project: f.project?.key ?? "", fields, updatedAt: jiraTime(f.updated), comments };
  }

  async search(query: string, page: Page): Promise<{ issues: Issue[]; hasMore: boolean }> {
    const c = await this.jira();
    const limit = Math.max(page.limit, 1);
    const offset = page.offset ?? 0;
    if (!this.cloud) {
      const r = await c.get<{ issues: ApiIssue[]; total: number }>("search", { jql: query, fields: "summary,updated,project", startAt: offset, maxResults: limit });
      return { issues: (r.issues ?? []).map((i) => this.issue(i)), hasMore: offset + (r.issues ?? []).length < (r.total ?? 0) };
    }
    let skip = offset;
    let token = "";
    const out: Issue[] = [];
    for (;;) {
      const r = await c.get<{ issues: ApiIssue[]; nextPageToken?: string; isLast?: boolean }>("search/jql", { jql: query, fields: "summary,updated,project", maxResults: Math.min(100, skip + limit - out.length + 1), nextPageToken: token || undefined });
      for (const i of r.issues ?? []) {
        if (skip > 0) {
          skip--;
          continue;
        }
        if (out.length === limit) return { issues: out, hasMore: true };
        out.push(this.issue(i));
      }
      if (!r.nextPageToken || r.isLast || !(r.issues ?? []).length) return { issues: out, hasMore: false };
      token = r.nextPageToken;
    }
  }

  async get(id: string): Promise<Issue> {
    const c = await this.jira();
    return this.issue(await c.get<ApiIssue>(`issue/${encodeURIComponent(id)}`, { fields: "summary,description,updated,project,status,priority,issuetype,assignee,labels,comment" }));
  }

  async create(d: IssueDraft): Promise<Issue> {
    const c = await this.jira();
    let kind = "Task";
    const rest: { name: string; value: string }[] = [];
    for (const f of d.fields ?? []) {
      if (/^(issue )?type$/i.test(f.name)) kind = f.value;
      else rest.push(f);
    }
    const fields: Record<string, unknown> = { project: { key: d.project }, summary: d.title, issuetype: { name: kind } };
    if (d.description) fields.description = d.description;
    const r = await c.send<{ key: string }>("POST", "issue", { fields });
    if (rest.length) await this.update(r.key, { fields: rest });
    return { id: r.key, title: d.title, url: `${this.site}/browse/${r.key}`, description: d.description ?? "", project: d.project, fields: [], updatedAt: "", comments: [] };
  }

  private async fieldDefs(fresh: boolean): Promise<ApiField[]> {
    if (!fresh && this.fieldList && Date.now() - this.fieldList.at < FIELDS_TTL_MS) return this.fieldList.list;
    const list = (await (await this.jira()).get<ApiField[]>("field")) ?? [];
    this.fieldList = { list, at: Date.now() };
    return list;
  }

  async update(id: string, u: IssueUpdate): Promise<void> {
    const c = await this.jira();
    const fields: Record<string, unknown> = {};
    const update: Record<string, unknown[]> = {};
    if (u.title !== undefined) fields.summary = u.title;
    if (u.description !== undefined) fields.description = u.description;
    let status = "";
    for (const f of u.fields ?? []) {
      if (isStatus(f.name)) status = f.value;
      else if (/^(add|remove) labels$/i.test(f.name)) {
        const verb = f.name.toLowerCase().startsWith("remove") ? "remove" : "add";
        update.labels = [...(update.labels ?? []), ...splitList(f.value).map((l) => ({ [verb]: l }))];
      } else {
        const [fid, v] = await this.encode(f);
        fields[fid] = v;
      }
    }
    if (Object.keys(fields).length || Object.keys(update).length) {
      await c.send("PUT", `issue/${encodeURIComponent(id)}`, { ...(Object.keys(fields).length ? { fields } : {}), ...(Object.keys(update).length ? { update } : {}) });
    }
    if (status) await this.transition(c, id, status);
  }

  private async transition(c: JsonClient, id: string, status: string): Promise<void> {
    const path = `issue/${encodeURIComponent(id)}/transitions`;
    const r = await c.get<{ transitions: { id: string; name: string; to?: { name?: string } }[] }>(path);
    const want = status.toLowerCase();
    const t = (r.transitions ?? []).find((x) => x.to?.name?.toLowerCase() === want || x.name.toLowerCase() === want);
    if (!t) throw new TrackerError("other", `${id} can't move to "${status}" from where it is; it can move to: ${(r.transitions ?? []).map((x) => x.to?.name).join(", ")}`);
    await c.send("POST", path, { transition: { id: t.id } });
  }

  private async encode(v: { name: string; value: string }): Promise<[string, unknown]> {
    const find = (list: ApiField[]) => list.find((f) => f.name.toLowerCase() === v.name.toLowerCase() || f.id === v.name);
    const f = find(await this.fieldDefs(false)) ?? find(await this.fieldDefs(true));
    if (!f) throw new TrackerError("other", `Unknown field "${v.name}"`);
    const type = f.schema?.type ?? "string";
    switch (type) {
      case "number": {
        const n = Number(v.value);
        if (!Number.isFinite(n)) throw new TrackerError("other", `Field "${v.name}": "${v.value}" is not a number`);
        return [f.id, n];
      }
      case "priority":
      case "issuetype":
      case "resolution":
      case "version":
      case "component":
        return [f.id, { name: v.value }];
      case "option":
        return [f.id, { value: v.value }];
      case "user":
        return [f.id, await this.user(v.value)];
      case "array": {
        const vals = splitList(v.value);
        if (f.schema?.items === "string") return [f.id, vals];
        if (f.schema?.items === "option") return [f.id, vals.map((x) => ({ value: x }))];
        if (f.schema?.items === "version" || f.schema?.items === "component") return [f.id, vals.map((x) => ({ name: x }))];
        throw new TrackerError("other", `Field "${v.name}": lists of ${f.schema?.items} can't be set from here`);
      }
      case "string":
      case "date":
      case "datetime":
        return [f.id, v.value];
    }
    throw new TrackerError("other", `Field "${v.name}": ${type} fields can't be set from here`);
  }

  private async user(who: string): Promise<Record<string, string> | null> {
    if (!who || /^(none|unassigned)$/i.test(who)) return null;
    const c = await this.jira();
    if (who.toLowerCase() === "me") {
      const me = await c.get<ApiUser>("myself");
      return this.cloud ? { accountId: me.accountId ?? "" } : { name: me.name ?? "" };
    }
    if (!this.cloud) return { name: who };
    const found = (await c.get<ApiUser[]>("user/search", { query: who })) ?? [];
    const w = who.toLowerCase();
    const hit = found.find((u) => u.emailAddress?.toLowerCase() === w || u.displayName?.toLowerCase() === w || u.accountId === who) ?? (found.length === 1 ? found[0] : undefined);
    if (!hit) throw new TrackerError("other", `No single user matches "${who}"`);
    return { accountId: hit.accountId ?? "" };
  }

  async comment(id: string, body: string): Promise<void> {
    await (await this.jira()).send("POST", `issue/${encodeURIComponent(id)}/comment`, { body });
  }

  async fields(project: string): Promise<Field[]> {
    const c = await this.jira();
    const status: Field = { name: "Status", kind: "state", multi: false, values: [] };
    if (project) {
      const types = (await c.get<{ statuses: { name: string }[] }[]>(`project/${encodeURIComponent(project)}/statuses`)) ?? [];
      for (const t of types) for (const s of t.statuses ?? []) if (!status.values.includes(s.name)) status.values.push(s.name);
    }
    const out: Field[] = [status];
    const prios = await c.get<{ name: string }[]>("priority").catch(() => undefined);
    if (prios) out.push({ name: "Priority", kind: "bundle", multi: false, values: prios.map((p) => p.name) });
    out.push(
      { name: "Assignee", kind: "user", multi: false, values: [] },
      { name: "Labels", kind: "labels", multi: true, values: [] },
      { name: "Add labels", kind: "labels", multi: true, values: [] },
      { name: "Remove labels", kind: "labels", multi: true, values: [] },
    );
    const custom = (await this.fieldDefs(false))
      .filter((f) => f.custom)
      .map((f): Field => ({ name: f.name, kind: fieldKind(f), multi: f.schema?.type === "array", values: [] }))
      .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
    return [...out, ...custom];
  }

  async projects(): Promise<ProviderProject[]> {
    const c = await this.jira();
    type P = { key: string; name: string; description?: string; avatarUrls?: Record<string, string>; archived?: boolean };
    let list: P[] = [];
    if (this.cloud) {
      for (let start = 0; start < 2000; ) {
        const r = await c.get<{ values: P[]; isLast?: boolean }>("project/search", { startAt: start, maxResults: 100 });
        list = list.concat(r.values ?? []);
        if (r.isLast || !(r.values ?? []).length) break;
        start += r.values.length;
      }
    } else list = (await c.get<P[]>("project")) ?? [];
    return list.filter((p) => p.key && !p.archived).map((p) => ({ key: p.key, name: p.name, description: p.description ?? "", logo: p.avatarUrls?.["48x48"] ?? "" }));
  }

  // ---- Confluence ----

  private article(a: ApiPage, web: string): Article {
    const base = a._links?.base || web;
    return {
      id: a.id,
      title: a.title,
      url: a._links?.webui ? `${base.replace(/\/+$/, "")}${a._links.webui}` : "",
      content: a.body?.storage?.value ? htmlText(a.body.storage.value) : "",
      project: a.space?.key ?? "",
      updatedAt: jiraTime(a.version?.when),
      parentId: a.ancestors?.length ? a.ancestors[a.ancestors.length - 1]!.id : "",
      hasChildren: (a.children?.page?.size ?? 0) > 0,
    };
  }

  async searchArticles(query: string, base: string, limit: number): Promise<Article[]> {
    const { c, web } = await this.wiki();
    const r = await c.get<{ results: ApiPage[] }>("content/search", { cql: cql(query, base), limit: Math.max(limit, 1), expand: "body.storage,space,version" });
    return (r.results ?? []).map((a) => this.article(a, web));
  }

  async listArticles(parentId: string, base: string, page: Page): Promise<{ articles: Article[]; hasMore: boolean }> {
    const { c, web } = await this.wiki();
    const q = parentId ? `type = page AND parent = ${JSON.stringify(parentId)} order by title` : `${cql("", base)} order by title`;
    const r = await c.get<{ results: ApiPage[] }>("content/search", { cql: q, start: page.offset ?? 0, limit: page.limit + 1, expand: "space,version,children.page" });
    const list = r.results ?? [];
    return { articles: list.slice(0, page.limit).map((a) => this.article(a, web)), hasMore: list.length > page.limit };
  }

  async getArticle(id: string): Promise<Article> {
    const { c, web } = await this.wiki();
    return this.article(await c.get<ApiPage>(`content/${encodeURIComponent(id)}`, { expand: "body.storage,space,version,ancestors,children.page" }), web);
  }
}

function fieldKind(f: ApiField): string {
  switch (f.schema?.type) {
    case "status":
      return "state";
    case "priority":
    case "option":
    case "issuetype":
    case "resolution":
      return "bundle";
    case "user":
      return "user";
    case "number":
      return "float";
    case "date":
    case "datetime":
      return "date";
    case "array":
      return f.schema.items === "string" ? "labels" : "bundle";
  }
  return f.schema?.system === "description" ? "text" : "string";
}

/** Opens Jira for a connection; knowledgeUrl is Confluence's own address (Data Center), "" for the site's /wiki on Cloud. */
export function openJira(o: OpenOptions, knowledgeUrl = ""): Jira {
  if (o.auth !== "email_token" && o.auth !== "token" && o.auth !== "oauth") throw new TrackerError("other", `Jira does not sign in with ${o.auth}`);
  const c = o.credentials;
  return new Jira({
    baseUrl: o.baseUrl,
    auth: o.auth,
    login: o.login,
    secret: c.token,
    oauth: o.auth === "oauth" ? { clientId: o.oauthClientId, clientSecret: c.clientSecret, accessToken: c.accessToken, refreshToken: c.refreshToken, expiresAt: c.expiresAt } : undefined,
    onOAuthTokens: o.onOAuthTokens,
    knowledgeUrl,
    allowPrivateNetwork: o.allowPrivateNetwork,
    retryDelaysMs: o.retryDelaysMs,
  });
}

/** Authorization Code with PKCE: Atlassian's hosts on Cloud, the instance's on Data Center. */
export const ATLASSIAN_OAUTH: OAuthModule = {
  async authorizeUrl({ baseUrl, clientId, redirectUri, state, challenge }) {
    return jiraOAuth(baseUrl).authorizeUrl({ clientId, redirectUri, state, challenge });
  },
  async exchangeCode({ baseUrl, clientId, clientSecret, code, redirectUri, verifier, allowPrivateNetwork }) {
    const ep = jiraOAuth(baseUrl);
    return oauthTokens(
      ep.tokenUrl,
      { grant_type: "authorization_code", client_id: clientId, code, redirect_uri: redirectUri, code_verifier: verifier, ...(clientSecret ? { client_secret: clientSecret } : {}) },
      { asJson: ep.cloud, allowPrivateNetwork, name: "Atlassian" },
    );
  },
};

export const JIRA: TrackerModule = {
  capabilities: JIRA_CAPABILITIES,
  open: (o) => openJira(o),
  oauth: ATLASSIAN_OAUTH,
};
