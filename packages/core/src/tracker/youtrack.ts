/**
 * YouTrack for admin connections: what packages/client/tracker/youtrack does on the client, for requests that must come from
 * the server. The behaviour follows xlsft/ymcp (MIT), including how it works without admin rights; see that package's notice.
 *
 * Every request goes through the SSRF guard. Nothing here logs, and error messages name the path and status, never a credential.
 */
import { guardedFetch } from "../lib/ssrf.ts";
import { freeTextTerms, TrackerError, type Article, type Capabilities, type Field, type Identity, type Issue, type IssueDraft, type IssueUpdate, type Page, type ProviderProject, type TrackerProvider } from "./provider.ts";

export const YOUTRACK_CAPABILITIES: Capabilities = {
  provider: "youtrack",
  displayName: "YouTrack",
  authMethods: ["token", "oauth", "password"],
  queryLanguage: "YouTrack search",
  queryExample: "project: DEMO Stage: Backlog",
  customFields: true,
  comments: true,
  knowledge: true,
};

export interface YouTrackCredentials {
  token?: string;
  login?: string;
  password?: string;
  oauth?: { clientId: string; clientSecret?: string; accessToken?: string; refreshToken?: string; expiresAt?: number };
}

export interface YouTrackOptions {
  baseUrl: string;
  auth: "token" | "password" | "oauth";
  credentials: YouTrackCredentials;
  allowPrivateNetwork: boolean;
  /** Tokens changed (an OAuth refresh): store them. */
  onOAuthTokens?: (t: { accessToken: string; refreshToken?: string; expiresAt: number }) => Promise<void>;
  retryDelaysMs?: number[];
}

const DEFAULT_RETRY_DELAYS = [500, 1000, 2000];
const SCHEMA_TTL_MS = 5 * 60_000;
const PROJECT_FIELD_FIELDS = "$type,canBeEmpty,emptyFieldText,field(name,fieldType(id,isMultiValue,valueType)),bundle(values(name,archived),aggregatedUsers(login))";
const ISSUE_LIST_FIELDS = "idReadable,summary,updated,project(shortName)";
const ISSUE_FULL_FIELDS = "idReadable,summary,description,updated,project(id,shortName,name),customFields(name,value(name,login,fullName,text,presentation))";
const ARTICLE_FIELDS = "idReadable,summary,content,updated,project(shortName),parentArticle(idReadable),childArticles(idReadable)";

interface ProjectField {
  $type?: string;
  field?: { name?: string; fieldType?: { id?: string; isMultiValue?: boolean; valueType?: string } };
  bundle?: { values?: { name: string; archived?: boolean }[]; aggregatedUsers?: { login: string }[] };
}

function fieldKind(f: ProjectField): string {
  switch (f.$type) {
    case "UserProjectCustomField":
      return "user";
    case "PeriodProjectCustomField":
      return "period";
    case "TextProjectCustomField":
      return "text";
    case "SimpleProjectCustomField":
      switch (f.field?.fieldType?.valueType) {
        case "date":
        case "date and time":
          return "date";
        case "integer":
          return "integer";
        case "float":
          return "float";
        default:
          return "string";
      }
    default:
      return "bundle";
  }
}

function fieldValues(f: ProjectField): string[] {
  return (f.bundle?.values ?? []).filter((v) => !v.archived).map((v) => v.name);
}

function issueFieldType(f: ProjectField): string {
  const base = (f.$type ?? "").replace(/ProjectCustomField$/, "");
  if (base === "State" || base === "Period" || base === "Text") return `${base}IssueCustomField`;
  if (base === "Simple") return fieldKind(f) === "date" ? "DateIssueCustomField" : "SimpleIssueCustomField";
  return `${f.field?.fieldType?.isMultiValue ? "Multi" : "Single"}${base}IssueCustomField`;
}

function encodeOne(f: ProjectField, name: string, raw: string): unknown {
  switch (fieldKind(f)) {
    case "integer":
    case "float":
    case "date": {
      const n = Number(raw);
      if (!Number.isFinite(n)) throw new TrackerError("other", `Field ${name}: ${raw} is not a number`);
      return n;
    }
    case "user": {
      const users = (f.bundle?.aggregatedUsers ?? []).map((u) => u.login);
      if (users.length && !users.includes(raw)) throw new TrackerError("other", `Field ${name}: user ${raw} can't be set here`);
      return { login: raw };
    }
    case "period":
      return { $type: "PeriodValue", presentation: raw };
    case "text":
      return { $type: "TextFieldValue", text: raw };
    case "string":
      return raw;
  }
  const values = fieldValues(f);
  if (values.length && !values.includes(raw)) throw new TrackerError("other", `Field ${name}: ${raw} is not allowed; allowed: ${values.join(", ")}`);
  return { name: raw };
}

function fieldValueText(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (Array.isArray(v)) return v.map(fieldValueText).filter(Boolean).join(", ");
  if (typeof v === "object") {
    const o = v as Record<string, unknown>;
    return String(o.name ?? o.fullName ?? o.login ?? o.text ?? o.presentation ?? "");
  }
  return String(v);
}

function iso(ms: unknown): string {
  return typeof ms === "number" && ms > 0 ? new Date(ms).toISOString() : "";
}

/** A body that is an HTML page, as a proxy answers, becomes its title; long ones are cut. */
function bodySummary(body: string): string {
  let b = body.trim();
  if (/^<!doctype html|<html/i.test(b)) {
    const m = /<title[^>]*>([\s\S]*?)<\/title>/i.exec(b);
    b = `(HTML page) ${(m ? m[1]! : b.replace(/<[^>]*>/g, " ")).replace(/\s+/g, " ").trim()}`;
  }
  return b.length > 300 ? `${b.slice(0, 300)}…` : b;
}

function statusKind(status: number): "not_found" | "auth" | "forbidden" | "transient" | "other" {
  if (status === 404) return "not_found";
  if (status === 401) return "auth";
  if (status === 403) return "forbidden";
  if (status === 429 || status >= 500) return "transient";
  return "other";
}

function encodeQuery(q: Record<string, string | number>): string {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) s.set(k, String(v));
  return s.toString();
}

export function normalizeYouTrackUrl(raw: string): string {
  let v = raw.trim().replace(/\/+$/, "");
  if (v && !v.includes("://")) v = `https://${v}`;
  return v.replace(/\/api$/, "");
}

export class YouTrack implements TrackerProvider {
  private readonly base: string;
  private readonly retryDelays: number[];
  private hub?: { url: string; serviceId: string };
  private cookies = new Map<string, string>();
  private passwordToken?: { value: string; expires: number };
  private schemas = new Map<string, { fields: Map<string, ProjectField>; at: number }>();

  constructor(private readonly o: YouTrackOptions) {
    this.base = normalizeYouTrackUrl(o.baseUrl);
    this.retryDelays = o.retryDelaysMs ?? DEFAULT_RETRY_DELAYS;
  }

  capabilities(): Capabilities {
    return YOUTRACK_CAPABILITIES;
  }

  // ---- Hub ----

  private async fetch(url: string, init: { method?: string; headers?: Record<string, string>; body?: string; manual?: boolean }) {
    try {
      return await guardedFetch(url, {
        method: init.method ?? "GET",
        headers: init.headers,
        body: init.body,
        allowPrivateNetwork: this.o.allowPrivateNetwork,
        followRedirects: !init.manual,
        signal: AbortSignal.timeout(30_000),
      });
    } catch (err) {
      if (err instanceof TrackerError) throw err;
      throw new TrackerError("transient", `Can't reach YouTrack: ${(err as Error).message}`);
    }
  }

  /** The Hub location and the YouTrack service id, from the public frontend config. */
  async discoverHub(): Promise<{ url: string; serviceId: string }> {
    if (this.hub) return this.hub;
    const res = await this.fetch(`${this.base}/api/config?fields=ring(url,serviceId)`, { headers: { accept: "application/json" } });
    const type = res.headers.get("content-type") ?? "";
    if (res.status !== 200 || !type.includes("json")) {
      await res.body?.cancel();
      throw new TrackerError(res.status >= 500 ? "transient" : "other", `Unexpected ${res.status} ${type} from ${new URL(this.base).host} (check the URL, VPN and proxy)`);
    }
    const cfg = (await res.json()) as { ring?: { url?: string; serviceId?: string } };
    if (!cfg.ring?.url || !cfg.ring.serviceId) throw new TrackerError("other", "The instance's config has no Hub URL or service id");
    this.hub = { url: new URL(cfg.ring.url, `${this.base}/`).href.replace(/\/+$/, ""), serviceId: cfg.ring.serviceId };
    return this.hub;
  }

  private storeCookies(res: { headers: { getSetCookie(): string[] } }) {
    for (const c of res.headers.getSetCookie()) {
      const [pair] = c.split(";");
      const eq = pair!.indexOf("=");
      if (eq > 0) this.cookies.set(pair!.slice(0, eq).trim(), pair!.slice(eq + 1));
    }
  }

  private cookieHeader(): Record<string, string> {
    return this.cookies.size ? { cookie: [...this.cookies].map(([k, v]) => `${k}=${v}`).join("; ") } : {};
  }

  /** Walks Hub's redirects until it sends the browser back to the instance with a token in the fragment. */
  private async followLogin(first: { url: string; method?: string; body?: string; headers?: Record<string, string> }, state: string, redirectUri: string): Promise<boolean> {
    let req: { url: string; method?: string; body?: string; headers?: Record<string, string> } = first;
    for (let hop = 0; hop < 10; hop++) {
      const res = await this.fetch(req.url, { method: req.method, body: req.body, headers: { ...req.headers, ...this.cookieHeader() }, manual: true });
      this.storeCookies(res);
      await res.body?.cancel();
      if (res.status < 300 || res.status >= 400) throw new TrackerError(res.status >= 500 ? "transient" : "other", `Hub login: unexpected ${res.status}`);
      const next = new URL(res.headers.get("location") ?? "", req.url);
      if (next.href.startsWith(redirectUri)) {
        const q = new URLSearchParams(next.hash.replace(/^#/, ""));
        if (q.get("error")) return false;
        if (q.get("state") !== state) throw new TrackerError("other", "Hub login: state mismatch");
        const token = q.get("access_token");
        if (!token) throw new TrackerError("other", "Hub login: no access token in the redirect");
        const ttl = Number(q.get("expires_in")) || 3600;
        this.passwordToken = { value: token, expires: Date.now() + (ttl - Math.min(ttl / 10, 60)) * 1000 };
        return true;
      }
      if (next.pathname.includes("/login/totp")) throw new TrackerError("auth", "This account uses two-factor authentication: use a token or OAuth instead");
      if (next.pathname.endsWith("/auth/login")) return false;
      req = { url: next.href };
    }
    throw new TrackerError("other", "Hub login: too many redirects");
  }

  private async passwordAccessToken(): Promise<string> {
    if (this.passwordToken && Date.now() < this.passwordToken.expires) return this.passwordToken.value;
    const hub = await this.discoverHub();
    const redirectUri = `${this.base}/oauth`;
    const query = (credentials: string, state: string) => encodeQuery({ response_type: "token", client_id: hub.serviceId, scope: hub.serviceId, redirect_uri: redirectUri, state, request_credentials: credentials });
    // The Hub session usually outlives the token, so without the password first.
    let state = crypto.randomUUID();
    if (await this.followLogin({ url: `${hub.url}/api/rest/oauth2/auth?${query("silent", state)}` }, state, redirectUri)) return this.passwordToken!.value;
    state = crypto.randomUUID();
    const ok = await this.followLogin(
      {
        url: `${hub.url}/api/rest/oauth2/interactive/login?${query("default", state)}`,
        method: "POST",
        headers: { "content-type": "application/x-www-form-urlencoded" },
        body: encodeQuery({ username: this.o.credentials.login ?? "", password: this.o.credentials.password ?? "" }),
      },
      state,
      redirectUri,
    );
    if (!ok) throw new TrackerError("auth", "YouTrack rejected the login or password");
    return this.passwordToken!.value;
  }

  private async oauthAccessToken(force = false): Promise<string> {
    const a = this.o.credentials.oauth;
    if (!a?.accessToken && !a?.refreshToken) throw new TrackerError("auth", "Authorize this connection first");
    if (!force && a.accessToken && (!a.expiresAt || Date.now() < a.expiresAt)) return a.accessToken;
    if (!a.refreshToken) throw new TrackerError("auth", "The authorization expired: authorize this connection again");
    const t = await this.exchangeToken({ grant_type: "refresh_token", refresh_token: a.refreshToken }, a.clientId, a.clientSecret);
    a.accessToken = t.accessToken;
    if (t.refreshToken) a.refreshToken = t.refreshToken;
    a.expiresAt = t.expiresAt;
    await this.o.onOAuthTokens?.({ accessToken: t.accessToken, refreshToken: a.refreshToken, expiresAt: t.expiresAt });
    return t.accessToken;
  }

  /** The token endpoint, for a refresh and for finishing Authorization Code. */
  async exchangeToken(form: Record<string, string>, clientId: string, clientSecret?: string): Promise<{ accessToken: string; refreshToken?: string; expiresAt: number }> {
    const hub = await this.discoverHub();
    const headers: Record<string, string> = { "content-type": "application/x-www-form-urlencoded", accept: "application/json" };
    const body: Record<string, string> = { ...form };
    if (clientSecret) headers.authorization = `Basic ${Buffer.from(`${clientId}:${clientSecret}`).toString("base64")}`;
    else body.client_id = clientId;
    const res = await this.fetch(`${hub.url}/api/rest/oauth2/token`, { method: "POST", headers, body: encodeQuery(body) });
    if (!res.ok) {
      await res.body?.cancel();
      throw new TrackerError(res.status >= 500 ? "transient" : "auth", `The token endpoint answered ${res.status}: authorize this connection again`);
    }
    const j = (await res.json()) as { access_token?: string; refresh_token?: string; expires_in?: number };
    if (!j.access_token) throw new TrackerError("auth", "The token endpoint gave no access token");
    const ttl = j.expires_in ?? 3600;
    return { accessToken: j.access_token, refreshToken: j.refresh_token, expiresAt: Date.now() + (ttl - Math.min(ttl / 10, 60)) * 1000 };
  }

  private async bearer(force = false): Promise<string> {
    switch (this.o.auth) {
      case "token":
        if (!this.o.credentials.token) throw new TrackerError("auth", "No token is stored for this connection");
        return this.o.credentials.token;
      case "password":
        if (force) this.passwordToken = undefined;
        return this.passwordAccessToken();
      case "oauth":
        return this.oauthAccessToken(force);
    }
  }

  // ---- REST ----

  private async request<T>(method: string, path: string, query: Record<string, string | number>, body?: unknown): Promise<T> {
    const q = encodeQuery(query);
    const url = `${this.base}/api/${path}${q ? `?${q}` : ""}`;
    const payload = body === undefined ? undefined : JSON.stringify(body);
    let renewed = false;
    for (let attempt = 0; ; attempt++) {
      let res;
      try {
        res = await this.fetch(url, {
          method,
          body: payload,
          headers: { authorization: `Bearer ${await this.bearer(renewed)}`, accept: "application/json", ...(payload ? { "content-type": "application/json" } : {}) },
        });
      } catch (err) {
        if (err instanceof TrackerError && err.kind === "transient" && method === "GET" && attempt < this.retryDelays.length) {
          await new Promise((r) => setTimeout(r, this.retryDelays[attempt]));
          continue;
        }
        throw err;
      }
      // 429 and 503 mean it did not take the request; other gateway errors may come after it acted, so only reads repeat.
      const retry = res.status === 429 || res.status === 503 || (method === "GET" && (res.status === 502 || res.status === 504));
      if (retry && attempt < this.retryDelays.length) {
        await res.body?.cancel();
        const after = Number(res.headers.get("retry-after"));
        await new Promise((r) => setTimeout(r, Number.isFinite(after) && after > 0 ? Math.min(after * 1000, 10_000) : this.retryDelays[attempt]));
        continue;
      }
      if (res.status === 401 && !renewed && this.o.auth !== "token") {
        await res.body?.cancel();
        renewed = true;
        continue;
      }
      const text = await res.text();
      if (!res.ok) {
        let msg = bodySummary(text);
        try {
          const j = JSON.parse(text) as { error?: string; error_description?: string };
          msg = j.error_description || j.error || msg;
        } catch {
          // Not JSON: the summary stands.
        }
        throw new TrackerError(statusKind(res.status), `YouTrack ${method} /api/${path}: ${res.status} ${msg}`);
      }
      if (!text) return undefined as T;
      if (!(res.headers.get("content-type") ?? "").includes("json")) {
        throw new TrackerError("other", `Unexpected ${res.headers.get("content-type")} answer (check the URL, VPN and proxy)`);
      }
      return JSON.parse(text) as T;
    }
  }

  async test(): Promise<Identity> {
    const me = await this.request<{ login?: string; fullName?: string }>("GET", "users/me", { fields: "login,fullName" });
    return { login: me.login ?? "", displayName: me.fullName ?? "" };
  }

  private issueUrl(id: string): string {
    return `${this.base}/issue/${id}`;
  }

  private toIssue(i: Record<string, unknown>): Issue {
    const id = String(i.idReadable ?? "");
    const fields = ((i.customFields as { name?: string; value?: unknown }[] | undefined) ?? [])
      .map((f) => ({ name: f.name ?? "", value: fieldValueText(f.value) }))
      .filter((f) => f.name && f.value);
    return {
      id,
      title: String(i.summary ?? ""),
      url: this.issueUrl(id),
      description: String(i.description ?? ""),
      project: String((i.project as { shortName?: string } | undefined)?.shortName ?? ""),
      fields,
      updatedAt: iso(i.updated),
      comments: [],
    };
  }

  async search(query: string, page: Page): Promise<{ issues: Issue[]; hasMore: boolean }> {
    const raw = await this.request<Record<string, unknown>[]>("GET", "issues", { query, fields: ISSUE_LIST_FIELDS, $skip: page.offset ?? 0, $top: page.limit + 1 });
    return { issues: raw.slice(0, page.limit).map((i) => this.toIssue(i)), hasMore: raw.length > page.limit };
  }

  async get(id: string): Promise<Issue> {
    const path = `issues/${encodeURIComponent(id)}`;
    const raw = await this.request<Record<string, unknown>>("GET", path, { fields: ISSUE_FULL_FIELDS });
    const issue = this.toIssue(raw);
    // Deleted comments stay in the list, without text.
    const comments = await this.request<{ text?: string; created?: number; deleted?: boolean; author?: { login?: string; fullName?: string } }[]>("GET", `${path}/comments`, {
      fields: "text,created,deleted,author(login,fullName)",
      $top: 200,
    });
    issue.comments = comments
      .filter((c) => !c.deleted)
      .slice(-10)
      .map((c) => ({ author: c.author?.fullName || c.author?.login || "", text: c.text ?? "", createdAt: iso(c.created) }));
    return issue;
  }

  private async project(shortName: string): Promise<{ id: string; shortName: string }> {
    const list = await this.request<{ id: string; shortName: string }[]>("GET", "admin/projects", { fields: "id,shortName", query: shortName, $top: 50 });
    const found = list.find((p) => p.shortName.toLowerCase() === shortName.toLowerCase());
    if (!found) throw new TrackerError("not_found", `No project ${shortName}`);
    return found;
  }

  /**
   * The custom fields of a project by name. The settings endpoint gives a user without admin rights on the project an empty
   * list or 403, so the schema then comes from recent issues; several, as conditional fields show only on issues they apply to.
   */
  private async loadSchema(project: { id: string; shortName: string }): Promise<Map<string, ProjectField>> {
    const out = new Map<string, ProjectField>();
    try {
      const list = await this.request<ProjectField[]>("GET", `admin/projects/${encodeURIComponent(project.id)}/customFields`, { fields: PROJECT_FIELD_FIELDS, $top: -1 });
      for (const f of list) if (f.field?.name) out.set(f.field.name, f);
    } catch (err) {
      if (!(err instanceof TrackerError && err.kind === "forbidden")) throw err;
    }
    if (out.size) return out;
    const issues = await this.request<{ customFields?: { projectCustomField?: ProjectField }[] }[]>("GET", "issues", {
      query: `project: {${project.shortName}} sort by: updated desc`,
      fields: `customFields(projectCustomField(${PROJECT_FIELD_FIELDS}))`,
      $top: 20,
    });
    for (const i of issues) {
      for (const cf of i.customFields ?? []) {
        const f = cf.projectCustomField;
        if (f?.field?.name && !out.has(f.field.name)) out.set(f.field.name, f);
      }
    }
    if (!out.size && !issues.length) throw new TrackerError("other", `Can't read the fields of ${project.shortName}: it has no issues, and this account can't read project settings`);
    return out;
  }

  private async schema(project: { id: string; shortName: string }, fresh = false): Promise<{ fields: Map<string, ProjectField>; cached: boolean }> {
    const c = this.schemas.get(project.id);
    if (!fresh && c && Date.now() - c.at < SCHEMA_TTL_MS) return { fields: c.fields, cached: true };
    const fields = await this.loadSchema(project);
    this.schemas.set(project.id, { fields, at: Date.now() });
    return { fields, cached: false };
  }

  private buildFields(schema: Map<string, ProjectField>, values: { name: string; value: string }[]) {
    return values.map(({ name, value }) => {
      const f = schema.get(name);
      if (!f) throw new TrackerError("other", `Unknown field ${name}; available: ${[...schema.keys()].sort().join(", ")}`);
      const multi = !!f.field?.fieldType?.isMultiValue;
      const encoded = multi ? value.split(",").map((v) => encodeOne(f, name, v.trim())) : encodeOne(f, name, value);
      return { name, $type: issueFieldType(f), value: encoded };
    });
  }

  /** A cached schema that rejects a value may be out of date, so it is checked once more against a fresh one. */
  private async customFields(project: { id: string; shortName: string }, values: { name: string; value: string }[]) {
    const { fields, cached } = await this.schema(project);
    try {
      return this.buildFields(fields, values);
    } catch (err) {
      if (!cached) throw err;
      return this.buildFields((await this.schema(project, true)).fields, values);
    }
  }

  async create(draft: IssueDraft): Promise<Issue> {
    const project = await this.project(draft.project);
    const body: Record<string, unknown> = { project: { id: project.id }, summary: draft.title, description: draft.description ?? "" };
    if (draft.fields?.length) body.customFields = await this.customFields(project, draft.fields);
    const created = await this.request<Record<string, unknown>>("POST", "issues", { fields: ISSUE_LIST_FIELDS }, body);
    return this.toIssue(created);
  }

  async update(id: string, update: IssueUpdate): Promise<void> {
    const body: Record<string, unknown> = {};
    if (update.title !== undefined) body.summary = update.title;
    if (update.description !== undefined) body.description = update.description;
    if (update.fields?.length) {
      const raw = await this.request<{ project?: { id: string; shortName: string } }>("GET", `issues/${encodeURIComponent(id)}`, { fields: "project(id,shortName)" });
      if (!raw.project) throw new TrackerError("other", `Issue ${id} has no project`);
      body.customFields = await this.customFields(raw.project, update.fields);
    }
    await this.request("POST", `issues/${encodeURIComponent(id)}`, { fields: "idReadable" }, body);
  }

  async comment(id: string, text: string): Promise<void> {
    await this.request("POST", `issues/${encodeURIComponent(id)}/comments`, { fields: "id" }, { text });
  }

  async fields(project: string): Promise<Field[]> {
    if (!project) return [];
    // A regular user may not list projects by admin path; the issues of the project name it as well.
    let ref: { id: string; shortName: string };
    try {
      ref = await this.project(project);
    } catch (err) {
      if (!(err instanceof TrackerError && (err.kind === "forbidden" || err.kind === "not_found"))) throw err;
      const issues = await this.request<{ project?: { id: string; shortName: string } }[]>("GET", "issues", { query: `project: {${project}}`, fields: "project(id,shortName)", $top: 1 });
      if (!issues[0]?.project) throw new TrackerError("not_found", `No project ${project} this account can see`);
      ref = issues[0].project;
    }
    const { fields } = await this.schema(ref);
    return [...fields.entries()]
      .map(([name, f]) => ({ name, kind: fieldKind(f), multi: !!f.field?.fieldType?.isMultiValue, values: fieldValues(f) }))
      .sort((a, b) => a.name.localeCompare(b.name));
  }

  /** What this account can read: the path says admin, the answer is per user. */
  async projects(): Promise<ProviderProject[]> {
    const list = await this.request<{ shortName?: string; name?: string; description?: string; iconUrl?: string; archived?: boolean }[]>("GET", "admin/projects", { fields: "shortName,name,description,iconUrl,archived", $top: 500 });
    return list
      .filter((p) => p.shortName && !p.archived)
      .map((p) => ({ key: p.shortName!, name: p.name ?? p.shortName!, description: p.description ?? "", logo: p.iconUrl ? new URL(p.iconUrl, `${this.base}/`).href : "" }));
  }

  // ---- Knowledge base ----

  private toArticle(a: Record<string, unknown>): Article {
    const id = String(a.idReadable ?? "");
    return {
      id,
      title: String(a.summary ?? ""),
      url: `${this.base}/articles/${id}`,
      content: String(a.content ?? ""),
      project: String((a.project as { shortName?: string } | undefined)?.shortName ?? ""),
      updatedAt: iso(a.updated),
      parentId: String((a.parentArticle as { idReadable?: string } | undefined)?.idReadable ?? ""),
      hasChildren: ((a.childArticles as unknown[] | undefined) ?? []).length > 0,
    };
  }

  /**
   * The public docs list only fields, $skip and $top for /api/articles, though instances take `query`. So what comes back is
   * also filtered here by the words of the query: an instance ignoring the parameter gives fewer hits rather than wrong ones.
   */
  async searchArticles(query: string, base: string, limit: number): Promise<Article[]> {
    const q = [base.trim(), query.trim()].filter(Boolean).join(" ");
    const raw = await this.request<Record<string, unknown>[]>("GET", "articles", { query: q, fields: ARTICLE_FIELDS, $top: Math.max(limit * 3, 20) });
    const terms = freeTextTerms(query);
    return raw
      .map((a) => this.toArticle(a))
      .filter((a) => {
        if (!terms.length) return true;
        const hay = `${a.title}\n${a.content}`.toLowerCase();
        return terms.some((t) => hay.includes(t));
      })
      .slice(0, limit);
  }

  async listArticles(parentId: string, base: string, page: Page): Promise<{ articles: Article[]; hasMore: boolean }> {
    const q: Record<string, string | number> = { fields: ARTICLE_FIELDS, $skip: page.offset ?? 0, $top: page.limit + 1 };
    const path = parentId ? `articles/${encodeURIComponent(parentId)}/childArticles` : "articles";
    if (!parentId && base.trim()) q.query = base.trim();
    const raw = await this.request<Record<string, unknown>[]>("GET", path, q);
    const hasMore = raw.length > page.limit;
    const all = raw.slice(0, page.limit).map((a) => this.toArticle(a));
    return { articles: parentId ? all : all.filter((a) => !a.parentId), hasMore };
  }

  async getArticle(id: string): Promise<Article> {
    return this.toArticle(await this.request<Record<string, unknown>>("GET", `articles/${encodeURIComponent(id)}`, { fields: ARTICLE_FIELDS }));
  }
}
