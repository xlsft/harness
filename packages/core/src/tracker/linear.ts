/**
 * Linear for company connections, over its GraphQL API: what packages/client/tracker/linear does on the client. Its documents are
 * the knowledge provider "linear-docs" (src/knowledge/linear-docs.ts), which borrows a Linear sign-in and uses the article
 * methods here. Sign-in: a personal API key as it is, or OAuth 2.0 with PKCE (https://linear.app/developers/graphql#authentication,
 * https://linear.app/developers/oauth-2-0-authentication).
 */
import { JsonClient, oauthTokens, splitList, TokenSource, TrackerHttpError, type OAuthTokens } from "./http.ts";
import { TrackerError, type Article, type Capabilities, type OAuthModule, type OpenOptions, type TrackerModule, type Field, type Identity, type Issue, type IssueDraft, type IssueUpdate, type Page, type ProviderProject, type TrackerProvider } from "./provider.ts";

export const LINEAR_API = "https://api.linear.app";

export const LINEAR_CAPABILITIES: Capabilities = {
  provider: "linear",
  displayName: "Linear",
  authMethods: ["token", "oauth"],
  queryLanguage: "Linear filter",
  queryExample: 'team:ENG state:"Todo" label:Bug',
  customFields: false,
  comments: true,
  defaultBaseUrl: LINEAR_API,
  docsUrl: "https://linear.app/docs/api-and-webhooks",
  logo: "linear",
  baseUrlPlaceholder: LINEAR_API,
  authHints: {
    token: { help: "A personal API key from Linear → Settings → Security & access → Personal API keys.", secretLabel: "API key" },
    oauth: { help: "An OAuth 2.0 application from Linear → Settings → API → OAuth applications." },
  },
};

export const PRIORITY_NAMES = ["No priority", "Urgent", "High", "Medium", "Low"];

const DOC = "id title content url updatedAt project { name }";
/** One listing reads at most this many: Linear pages by cursor, an offset is read through from the start. */
const MAX_DOCUMENTS = 250;

type ApiDocument = { id: string; title: string; content?: string; url: string; updatedAt: string; project?: { name: string } | null };

const toArticle = (d: ApiDocument): Article => ({ id: d.id, title: d.title, url: d.url, content: d.content ?? "", project: d.project?.name ?? "", updatedAt: d.updatedAt, parentId: "", hasChildren: false });

/** The project name a source's narrowing names, `project: Handbook` or `Handbook`; "" for every document. */
export function linearDocumentProject(base: string): string {
  return base
    .trim()
    .replace(/^project:/i, "")
    .trim()
    .replace(/^["']|["']$/g, "");
}

const inProject = (d: ApiDocument, project: string) => !project || (d.project?.name ?? "").trim().toLowerCase() === project.toLowerCase();

export function linearApiBase(raw: string): string {
  const b = raw.trim().replace(/\/+$/, "").replace(/\/graphql$/, "");
  return b || LINEAR_API;
}

/** Linear's consent page is on linear.app, its API on api.linear.app; anything else (a fake) serves both. */
export function linearOAuth(baseUrl: string) {
  const api = linearApiBase(baseUrl);
  return {
    tokenUrl: `${api}/oauth/token`,
    authorizeUrl(p: { clientId: string; redirectUri: string; state: string; challenge: string }): string {
      const q = new URLSearchParams({ client_id: p.clientId, redirect_uri: p.redirectUri, state: p.state, response_type: "code", scope: "read,write", code_challenge: p.challenge, code_challenge_method: "S256", prompt: "consent" });
      return `${api === LINEAR_API ? "https://linear.app" : api}/oauth/authorize?${q}`;
    },
  };
}

export interface LinearOptions {
  baseUrl: string;
  auth: "token" | "oauth";
  secret?: string;
  oauth?: { clientId: string; clientSecret?: string; accessToken?: string; refreshToken?: string; expiresAt?: number };
  onOAuthTokens?: (t: OAuthTokens) => Promise<void>;
  allowPrivateNetwork: boolean;
  retryDelaysMs?: number[];
}

/** Splits on spaces outside quotes, keeping the quotes. */
function tokens(s: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quoted = false;
  for (const ch of s) {
    if (ch === '"') {
      quoted = !quoted;
      cur += ch;
    } else if (/\s/.test(ch) && !quoted) {
      if (cur) out.push(cur);
      cur = "";
    } else cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

function priorityNumber(v: string): number {
  const n = Number(v);
  if (/^\d$/.test(v) && n >= 0 && n <= 4) return n;
  const i = PRIORITY_NAMES.findIndex((p) => p.toLowerCase() === v.toLowerCase());
  if (i >= 0) return i;
  if (/^(none|no)$/i.test(v)) return 0;
  throw new TrackerError("other", `priority: "${v}" is not 0 to 4 or one of ${PRIORITY_NAMES.join(", ")}`);
}

function keyFilter(k: string, v: string): Record<string, unknown> {
  const eq = (field: string) => ({ [field]: { eqIgnoreCase: v } });
  switch (k) {
    case "team":
      return { team: { or: [{ key: { eqIgnoreCase: v } }, eq("name")] } };
    case "state":
    case "status":
      return { state: { or: [eq("name"), { type: { eq: v.toLowerCase() } }] } };
    case "assignee":
      if (v.toLowerCase() === "me") return { assignee: { isMe: { eq: true } } };
      if (/^(none|unassigned)$/i.test(v)) return { assignee: { null: true } };
      return { assignee: { or: [{ email: { eqIgnoreCase: v } }, eq("name"), eq("displayName")] } };
    case "label":
    case "labels":
      return { labels: { some: eq("name") } };
    case "project":
      return { project: eq("name") };
    case "priority":
      return { priority: { eq: priorityNumber(v) } };
    case "cycle": {
      if (/^(current|active)$/i.test(v)) return { cycle: { isActive: { eq: true } } };
      const n = Number(v);
      if (!Number.isInteger(n)) throw new TrackerError("other", `cycle: "${v}" is not current or a number`);
      return { cycle: { number: { eq: n } } };
    }
  }
  throw new TrackerError("other", `Unknown filter "${k}": use team, state, assignee, label, project, priority or cycle, or an IssueFilter as JSON`);
}

/** A stage input's query as an IssueFilter: JSON, or the short form `team:ENG state:"In Progress" assignee:me` (see the Go side). */
export function parseLinearFilter(q: string): Record<string, unknown> {
  const s = q.trim();
  if (s.startsWith("{")) {
    try {
      return JSON.parse(s) as Record<string, unknown>;
    } catch (err) {
      throw new TrackerError("other", `The filter is not valid JSON: ${(err as Error).message}`);
    }
  }
  const values = new Map<string, string[]>();
  const words: string[] = [];
  for (const tok of tokens(s)) {
    const i = tok.indexOf(":");
    if (i <= 0) {
      words.push(tok);
      continue;
    }
    const k = tok.slice(0, i).toLowerCase();
    values.set(k, [...(values.get(k) ?? []), tok.slice(i + 1).replace(/^"|"$/g, "")]);
  }
  const and: Record<string, unknown>[] = [];
  for (const [k, vs] of values) {
    const alts = vs.map((v) => keyFilter(k, v));
    and.push(alts.length === 1 ? alts[0]! : { or: alts });
  }
  for (const w of words) and.push({ title: { containsIgnoreCase: w.replace(/^"|"$/g, "") } });
  if (!and.length) return {};
  return and.length === 1 ? and[0]! : { and };
}

interface ApiIssue {
  id: string;
  identifier: string;
  title: string;
  url: string;
  description?: string;
  updatedAt?: string;
  priority?: number;
  team?: { id: string; key: string };
  state?: { name: string };
  assignee?: { name: string } | null;
  project?: { name: string } | null;
  labels?: { nodes: { id: string; name: string }[] };
  comments?: { nodes: { body: string; createdAt: string; user?: { name: string } | null }[] };
}

interface Team {
  id: string;
  key: string;
  name: string;
  states: { nodes: { id: string; name: string }[] };
  labels: { nodes: { id: string; name: string }[] };
  members: { nodes: { id: string; name: string; displayName: string; email: string }[] };
}

const LIST = "identifier title url updatedAt team { key }";
const FULL = "id identifier title url description updatedAt priority team { id key } state { name } assignee { name } project { name } labels { nodes { id name } } comments(last: 10) { nodes { body createdAt user { name } } }";

function toIssue(i: ApiIssue): Issue {
  const fields: { name: string; value: string }[] = [];
  const add = (name: string, v: string | undefined) => v && fields.push({ name, value: v });
  add("State", i.state?.name);
  if (i.priority && i.priority > 0) add("Priority", PRIORITY_NAMES[i.priority]);
  add("Assignee", i.assignee?.name);
  add("Project", i.project?.name);
  add("Labels", (i.labels?.nodes ?? []).map((l) => l.name).join(", "));
  return {
    id: i.identifier,
    title: i.title,
    url: i.url,
    description: i.description ?? "",
    project: i.team?.key ?? "",
    fields,
    updatedAt: i.updatedAt ?? "",
    comments: (i.comments?.nodes ?? []).map((c) => ({ author: c.user?.name ?? "", text: c.body, createdAt: c.createdAt })),
  };
}

export class Linear implements TrackerProvider {
  private readonly c: JsonClient;
  private readonly tokens?: TokenSource;

  constructor(private readonly o: LinearOptions) {
    const base = linearApiBase(o.baseUrl);
    if (o.auth === "oauth") {
      const a = o.oauth ?? { clientId: "" };
      this.tokens = new TokenSource(
        { accessToken: a.accessToken, refreshToken: a.refreshToken, expiresAt: a.expiresAt },
        (refresh) => oauthTokens(`${base}/oauth/token`, { grant_type: "refresh_token", client_id: a.clientId, refresh_token: refresh, ...(a.clientSecret ? { client_secret: a.clientSecret } : {}) }, { asJson: false, allowPrivateNetwork: o.allowPrivateNetwork, name: "Linear" }),
        o.onOAuthTokens,
      );
    }
    this.c = new JsonClient({ base, name: "Linear", allowPrivateNetwork: o.allowPrivateNetwork, retryDelaysMs: o.retryDelaysMs, authorize: () => this.authorize() });
  }

  private async authorize(): Promise<Record<string, string>> {
    if (this.o.auth === "token") {
      if (!this.o.secret) throw new TrackerError("auth", "No API key is stored for this connection");
      return { authorization: this.o.secret };
    }
    return { authorization: `Bearer ${await this.tokens!.token()}` };
  }

  capabilities(): Capabilities {
    return LINEAR_CAPABILITIES;
  }

  private async gql<T>(query: string, variables?: Record<string, unknown>): Promise<T> {
    type Res = { data?: T; errors?: { message: string; extensions?: { code?: string; type?: string } }[] };
    let res: Res;
    try {
      res = await this.c.send<Res>("POST", "graphql", { query, variables: variables ?? {} });
    } catch (err) {
      if (!(err instanceof TrackerHttpError) || err.detail.status !== 400) throw err;
      try {
        res = JSON.parse(err.detail.body) as Res;
      } catch {
        throw err;
      }
      if (!res.errors?.length) throw err;
    }
    const e = res.errors?.[0];
    if (e) {
      const msg = `Linear: ${e.message}`;
      switch ((e.extensions?.code ?? "").toUpperCase()) {
        case "AUTHENTICATION_ERROR":
          throw new TrackerError("auth", msg);
        case "FORBIDDEN":
          throw new TrackerError("forbidden", msg);
        case "RATELIMITED":
          throw new TrackerError("transient", msg);
      }
      if (/not found/i.test(e.message)) throw new TrackerError("not_found", msg);
      throw new TrackerError("other", msg);
    }
    return res.data as T;
  }

  async test(): Promise<Identity> {
    const d = await this.gql<{ viewer: { name: string; displayName: string; email: string } }>("query { viewer { name displayName email } }");
    return { login: d.viewer.email || d.viewer.displayName, displayName: d.viewer.name };
  }

  async search(query: string, page: Page): Promise<{ issues: Issue[]; hasMore: boolean }> {
    const filter = parseLinearFilter(query);
    const limit = Math.max(page.limit, 1);
    let skip = page.offset ?? 0;
    let after = "";
    const out: Issue[] = [];
    for (;;) {
      const d = await this.gql<{ issues: { nodes: ApiIssue[]; pageInfo: { hasNextPage: boolean; endCursor: string } } }>(
        `query($filter: IssueFilter, $first: Int, $after: String) { issues(filter: $filter, first: $first, after: $after, orderBy: createdAt) { nodes { ${LIST} } pageInfo { hasNextPage endCursor } } }`,
        { filter, first: Math.min(100, skip + limit - out.length + 1), ...(after ? { after } : {}) },
      );
      for (const i of d.issues.nodes) {
        if (skip > 0) {
          skip--;
          continue;
        }
        if (out.length === limit) return { issues: out, hasMore: true };
        out.push(toIssue(i));
      }
      if (!d.issues.pageInfo.hasNextPage || !d.issues.nodes.length) return { issues: out, hasMore: false };
      after = d.issues.pageInfo.endCursor;
    }
  }

  private async issue(id: string): Promise<ApiIssue> {
    const d = await this.gql<{ issue: ApiIssue | null }>(`query($id: String!) { issue(id: $id) { ${FULL} } }`, { id });
    if (!d.issue) throw new TrackerError("not_found", `Issue ${id} not found`);
    return d.issue;
  }

  async get(id: string): Promise<Issue> {
    return toIssue(await this.issue(id));
  }

  private async team(idOrKey: string): Promise<Team> {
    const d = await this.gql<{ teams: { nodes: Team[] } }>(
      "query($f: TeamFilter) { teams(filter: $f, first: 2) { nodes { id key name states { nodes { id name } } labels(first: 250) { nodes { id name } } members(first: 250) { nodes { id name displayName email } } } } }",
      { f: { or: [{ key: { eqIgnoreCase: idOrKey } }, { id: { eq: idOrKey } }] } },
    );
    const t = d.teams.nodes[0];
    if (!t) throw new TrackerError("not_found", `No team ${idOrKey} this account can see`);
    return t;
  }

  private async input(t: Team, current: string[], fields: { name: string; value: string }[]): Promise<Record<string, unknown>> {
    const input: Record<string, unknown> = {};
    let labels = [...current];
    let labelsChanged = false;
    const labelId = (name: string) => {
      const l = t.labels.nodes.find((x) => x.name.toLowerCase() === name.toLowerCase());
      if (!l) throw new TrackerError("other", `No label "${name}" in ${t.key}`);
      return l.id;
    };
    for (const f of fields) {
      const name = f.name.trim().toLowerCase();
      switch (name) {
        case "state":
        case "status":
        case "stage": {
          const s = t.states.nodes.find((x) => x.name.toLowerCase() === f.value.toLowerCase());
          if (!s) throw new TrackerError("other", `"${f.value}" is not a state of ${t.key}; its states: ${t.states.nodes.map((x) => x.name).join(", ")}`);
          input.stateId = s.id;
          break;
        }
        case "priority":
          input.priority = priorityNumber(f.value);
          break;
        case "assignee": {
          const v = f.value.trim();
          if (!v || /^(none|unassigned)$/i.test(v)) input.assigneeId = null;
          else if (v.toLowerCase() === "me") input.assigneeId = (await this.gql<{ viewer: { id: string } }>("query { viewer { id } }")).viewer.id;
          else {
            const m = t.members.nodes.find((x) => [x.email, x.name, x.displayName].some((s) => s?.toLowerCase() === v.toLowerCase()));
            if (!m) throw new TrackerError("other", `No member of ${t.key} is "${v}"`);
            input.assigneeId = m.id;
          }
          break;
        }
        case "labels":
        case "add labels":
        case "remove labels":
          if (name === "labels") labels = [];
          labelsChanged = true;
          for (const l of splitList(f.value)) {
            const id = labelId(l);
            if (name === "remove labels") labels = labels.filter((x) => x !== id);
            else if (!labels.includes(id)) labels.push(id);
          }
          break;
        case "title":
          input.title = f.value;
          break;
        case "description":
          input.description = f.value;
          break;
        default:
          throw new TrackerError("other", `Unknown field "${f.name}": Linear sets State, Priority, Assignee, Labels, Add labels, Remove labels, Title and Description`);
      }
    }
    if (labelsChanged) input.labelIds = labels;
    return input;
  }

  private async mutate(query: string, variables: Record<string, unknown>, field: string): Promise<{ issue?: ApiIssue }> {
    const d = await this.gql<Record<string, { success: boolean; issue?: ApiIssue }>>(query, variables);
    if (!d[field]?.success) throw new TrackerError("other", `Linear did not accept ${field}`);
    return d[field];
  }

  async create(draft: IssueDraft): Promise<Issue> {
    const t = await this.team(draft.project);
    const input = await this.input(t, [], draft.fields ?? []);
    Object.assign(input, { teamId: t.id, title: draft.title, ...(draft.description ? { description: draft.description } : {}) });
    const r = await this.mutate(`mutation($in: IssueCreateInput!) { issueCreate(input: $in) { success issue { ${LIST} } } }`, { in: input }, "issueCreate");
    return toIssue(r.issue!);
  }

  async update(id: string, u: IssueUpdate): Promise<void> {
    const cur = await this.issue(id);
    const fields = [...(u.fields ?? [])];
    if (u.title !== undefined) fields.push({ name: "Title", value: u.title });
    if (u.description !== undefined) fields.push({ name: "Description", value: u.description });
    if (!fields.length) return;
    if (!cur.team) throw new TrackerError("other", "Linear did not say which team the issue is in");
    const t = await this.team(cur.team.id);
    const input = await this.input(
      t,
      (cur.labels?.nodes ?? []).map((l) => l.id),
      fields,
    );
    await this.mutate("mutation($id: String!, $in: IssueUpdateInput!) { issueUpdate(id: $id, input: $in) { success } }", { id: cur.id, in: input }, "issueUpdate");
  }

  async comment(id: string, body: string): Promise<void> {
    const cur = await this.issue(id);
    await this.mutate("mutation($in: CommentCreateInput!) { commentCreate(input: $in) { success } }", { in: { issueId: cur.id, body } }, "commentCreate");
  }

  async fields(project: string): Promise<Field[]> {
    const state: Field = { name: "State", kind: "state", multi: false, values: [] };
    const assignee: Field = { name: "Assignee", kind: "user", multi: false, values: [] };
    const labels: Field = { name: "Labels", kind: "labels", multi: true, values: [] };
    if (project) {
      const t = await this.team(project);
      state.values = t.states.nodes.map((s) => s.name);
      assignee.values = t.members.nodes.map((m) => m.email || m.name);
      labels.values = t.labels.nodes.map((l) => l.name);
    }
    return [state, { name: "Priority", kind: "bundle", multi: false, values: PRIORITY_NAMES }, assignee, labels, { ...labels, name: "Add labels" }, { ...labels, name: "Remove labels" }];
  }

  async projects(): Promise<ProviderProject[]> {
    const d = await this.gql<{ teams: { nodes: { key: string; name: string; description?: string }[] } }>("query { teams(first: 250) { nodes { key name description icon } } }");
    return d.teams.nodes.map((t) => ({ key: t.key, name: t.name, description: t.description ?? "", logo: "" }));
  }

  /** Linear's document search; the narrowing is applied to what it finds. */
  async searchArticles(query: string, base: string, limit: number): Promise<Article[]> {
    limit = Math.max(limit, 1);
    const project = linearDocumentProject(base);
    const first = project ? Math.min(limit * 4, 100) : limit;
    const d = await this.gql<{ searchDocuments: { nodes: ApiDocument[] } }>(`query($term: String!, $first: Int) { searchDocuments(term: $term, first: $first) { nodes { ${DOC} } } }`, { term: query, first });
    return d.searchDocuments.nodes.filter((n) => inProject(n, project)).slice(0, limit).map(toArticle);
  }

  /** The documents, most recently updated first. They have no children. */
  async listArticles(parentId: string, base: string, page: Page): Promise<{ articles: Article[]; hasMore: boolean }> {
    if (parentId) return { articles: [], hasMore: false };
    const limit = page.limit > 0 ? page.limit : 100;
    const offset = page.offset ?? 0;
    const want = Math.min(offset + limit + 1, MAX_DOCUMENTS);
    const project = linearDocumentProject(base);
    const vars: Record<string, unknown> = project ? { filter: { project: { name: { eqIgnoreCase: project } } } } : {};
    const q = `query($first: Int, $after: String${project ? ", $filter: DocumentFilter" : ""}) { documents(first: $first, after: $after, orderBy: updatedAt${project ? ", filter: $filter" : ""}) { nodes { ${DOC} } pageInfo { hasNextPage endCursor } } }`;
    const all: ApiDocument[] = [];
    let after: string | null = null;
    while (all.length < want) {
      type Res = { documents: { nodes: ApiDocument[]; pageInfo: { hasNextPage: boolean; endCursor: string } } };
      const d: Res = await this.gql<Res>(q, { ...vars, first: Math.min(want - all.length, 100), after });
      all.push(...d.documents.nodes);
      if (!d.documents.pageInfo.hasNextPage || !d.documents.pageInfo.endCursor || !d.documents.nodes.length) break;
      after = d.documents.pageInfo.endCursor;
    }
    const end = Math.min(offset + limit, all.length);
    return { articles: all.slice(offset, end).map(toArticle), hasMore: all.length > end };
  }

  async getArticle(id: string): Promise<Article> {
    const d = await this.gql<{ document: ApiDocument | null }>(`query($id: String!) { document(id: $id) { ${DOC} } }`, { id });
    if (!d.document) throw new TrackerError("not_found", `Document ${id} not found`);
    return toArticle(d.document);
  }
}

export function openLinear(o: OpenOptions): Linear {
  if (o.auth !== "token" && o.auth !== "oauth") throw new TrackerError("other", `Linear does not sign in with ${o.auth}`);
  const c = o.credentials;
  return new Linear({
    baseUrl: o.baseUrl,
    auth: o.auth,
    secret: c.token,
    oauth: o.auth === "oauth" ? { clientId: o.oauthClientId, clientSecret: c.clientSecret, accessToken: c.accessToken, refreshToken: c.refreshToken, expiresAt: c.expiresAt } : undefined,
    onOAuthTokens: o.onOAuthTokens,
    allowPrivateNetwork: o.allowPrivateNetwork,
    retryDelaysMs: o.retryDelaysMs,
  });
}

export const LINEAR_OAUTH: OAuthModule = {
  async authorizeUrl({ baseUrl, clientId, redirectUri, state, challenge }) {
    return linearOAuth(baseUrl).authorizeUrl({ clientId, redirectUri, state, challenge });
  },
  async exchangeCode({ baseUrl, clientId, clientSecret, code, redirectUri, verifier, allowPrivateNetwork }) {
    return oauthTokens(
      linearOAuth(baseUrl).tokenUrl,
      { grant_type: "authorization_code", client_id: clientId, code, redirect_uri: redirectUri, code_verifier: verifier, ...(clientSecret ? { client_secret: clientSecret } : {}) },
      { asJson: false, allowPrivateNetwork, name: "Linear" },
    );
  },
};

export const LINEAR: TrackerModule = {
  capabilities: LINEAR_CAPABILITIES,
  open: openLinear,
  oauth: LINEAR_OAUTH,
  normalizeUrl: linearApiBase,
};
