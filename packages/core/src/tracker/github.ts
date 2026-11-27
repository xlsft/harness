/**
 * GitHub Issues for company connections. It signs in through a code host connection from Settings > VCS (the "vcs" method):
 * the installation token of its GitHub App for the account in "vcs_owner", or the first installation. Comments and changes
 * are the app's. The client's packages/client/tracker/github does the same with the gh login. GitHub has no knowledge base
 * here. docs/design/tracker.md, section "GitHub Issues".
 */
import { githubApiBase } from "../vcs/github-app.ts";
import { JsonClient, splitList } from "./http.ts";
import { VCS_OPTION_FIELDS, vcsSignIn, vcsTrackerError } from "./vcs-signin.ts";
import {
  TrackerError,
  type Capabilities,
  type Field,
  type Identity,
  type Issue,
  type IssueDraft,
  type IssueUpdate,
  type OpenOptions,
  type Page,
  type ProviderProject,
  type TrackerModule,
  type TrackerProvider,
  type VcsSignIn,
} from "./provider.ts";

export const GITHUB_CAPABILITIES: Capabilities = {
  provider: "github",
  displayName: "GitHub Issues",
  authMethods: ["vcs"],
  queryLanguage: "GitHub search",
  queryExample: "repo:octo/app is:open label:bug",
  customFields: false,
  comments: true,
  optionFields: VCS_OPTION_FIELDS,
  docsUrl: "https://docs.github.com/en/rest/issues",
  logo: "github",
  baseUrlPlaceholder: "https://github.com",
  defaultBaseUrl: "https://github.com",
  authHints: { vcs: { help: "Signs in through GitHub: on this device the gh command line (gh auth login), for the company a connection from Settings → VCS." } },
};

/** Splits owner/repo#123. */
export function parseGitHubId(id: string): { repo: string; owner: string; name: string; number: number } {
  const m = /^([^/\s#]+)\/([^/\s#]+)#(\d+)$/.exec(id.trim());
  if (!m) throw new TrackerError("not_found", `"${id}" is not owner/repo#number`);
  return { owner: m[1]!, name: m[2]!, repo: `${m[1]}/${m[2]}`, number: Number(m[3]) };
}

/** Keeps pull requests out unless the query asks for them. */
export function githubSearchQuery(q: string): string {
  const low = ` ${q.toLowerCase()} `;
  return /\s(is:issue|is:pr|is:pull-request|type:)/.test(low) ? q.trim() : `${q.trim()} is:issue`;
}

interface ApiIssue {
  number: number;
  title: string;
  body?: string | null;
  html_url: string;
  state: string;
  state_reason?: string | null;
  updated_at?: string;
  repository_url?: string;
  pull_request?: unknown;
  labels?: { name: string }[];
  assignees?: { login: string }[];
  milestone?: { title: string } | null;
}

function toIssue(i: ApiIssue, repo?: string): Issue {
  const r = repo || (i.repository_url ?? "").split("/repos/")[1] || "";
  const fields: { name: string; value: string }[] = [];
  const add = (name: string, v: string | undefined) => v && fields.push({ name, value: v });
  add("State", i.state === "closed" && i.state_reason === "not_planned" ? "closed as not planned" : i.state);
  add("Labels", (i.labels ?? []).map((l) => l.name).join(", "));
  add("Assignees", (i.assignees ?? []).map((a) => a.login).join(", "));
  add("Milestone", i.milestone?.title);
  return { id: `${r}#${i.number}`, title: i.title, url: i.html_url, description: i.body ?? "", project: r, fields, updatedAt: i.updated_at ?? "", comments: [] };
}

export interface GitHubOptions {
  /** The code host connection's sign-in; asked for once, its token whenever a request needs one. */
  signIn: () => Promise<VcsSignIn>;
  allowPrivateNetwork: boolean;
  retryDelaysMs?: number[];
}

export class GitHubIssues implements TrackerProvider {
  private client?: Promise<{ c: JsonClient; type: string }>;

  constructor(private readonly o: GitHubOptions) {}

  capabilities(): Capabilities {
    return GITHUB_CAPABILITIES;
  }

  /** The client of the connection's API. The code host's errors read as a tracker's. */
  private async api(): Promise<JsonClient> {
    this.client ??= this.o
      .signIn()
      .catch(vcsTrackerError)
      .then((v) => ({
        type: v.type,
        c: new JsonClient({
          base: githubApiBase(v.baseUrl.replace(/\/+$/, "")),
          name: "GitHub",
          allowPrivateNetwork: this.o.allowPrivateNetwork,
          retryDelaysMs: this.o.retryDelaysMs,
          headers: { accept: "application/vnd.github+json", "x-github-api-version": "2022-11-28" },
          authorize: async () => ({ authorization: `Bearer ${await v.token().catch(vcsTrackerError)}` }),
        }),
      }));
    const { c, type } = await this.client.catch((err: unknown) => {
      this.client = undefined;
      throw err;
    });
    if (type !== "github") throw new TrackerError("other", "GitHub Issues signs in through a GitHub connection, not a GitLab one");
    return c;
  }

  async test(): Promise<Identity> {
    const c = await this.api();
    await c.get("installation/repositories", { per_page: 1 });
    return { login: "GitHub App", displayName: "GitHub App" };
  }

  /** GitHub's search syntax, over the repositories the installation sees. */
  async search(query: string, page: Page): Promise<{ issues: Issue[]; hasMore: boolean }> {
    const limit = Math.max(page.limit, 1);
    const c = await this.api();
    const out: Issue[] = [];
    let skip = page.offset ?? 0;
    for (let n = 1; n <= 10; n++) {
      const r = await c.get<{ total_count: number; items: ApiIssue[] }>("search/issues", { q: githubSearchQuery(query), per_page: 100, page: n });
      for (const i of r.items ?? []) {
        if (i.pull_request) continue;
        if (skip > 0) {
          skip--;
          continue;
        }
        if (out.length === limit) return { issues: out, hasMore: true };
        out.push(toIssue(i));
      }
      if ((r.items ?? []).length < 100 || n * 100 >= r.total_count) break;
    }
    return { issues: out, hasMore: false };
  }

  async get(id: string): Promise<Issue> {
    const r = parseGitHubId(id);
    const c = await this.api();
    const path = `repos/${r.owner}/${r.name}/issues/${r.number}`;
    const issue = toIssue(await c.get<ApiIssue>(path), r.repo);
    const comments = (await c.get<{ body: string; created_at: string; user?: { login: string } }[]>(`${path}/comments`, { per_page: 100 })) ?? [];
    issue.comments = comments.map((x) => ({ author: x.user?.login ?? "", text: x.body, createdAt: x.created_at })).slice(-10);
    return issue;
  }

  async create(d: IssueDraft): Promise<Issue> {
    const m = /^([^/\s]+)\/([^/\s]+)$/.exec(d.project);
    if (!m) throw new TrackerError("other", `"${d.project}" is not owner/repo`);
    const created = toIssue(await (await this.api()).send<ApiIssue>("POST", `repos/${m[1]}/${m[2]}/issues`, { title: d.title, body: d.description ?? "" }), d.project);
    if (d.fields?.length) await this.update(created.id, { fields: d.fields });
    return created;
  }

  async update(id: string, u: IssueUpdate): Promise<void> {
    const r = parseGitHubId(id);
    const c = await this.api();
    const path = `repos/${r.owner}/${r.name}/issues/${r.number}`;
    const body: Record<string, unknown> = {};
    if (u.title !== undefined) body.title = u.title;
    if (u.description !== undefined) body.body = u.description;
    const add: string[] = [];
    const rm: string[] = [];
    for (const f of u.fields ?? []) {
      switch (f.name.trim().toLowerCase()) {
        case "state":
        case "status":
        case "stage": {
          const v = f.value.trim().toLowerCase();
          if (v === "open" || v === "reopened") body.state = "open";
          else if (["closed", "completed", "done", "closed as completed"].includes(v)) Object.assign(body, { state: "closed", state_reason: "completed" });
          else if (["not planned", "closed as not planned", "not_planned"].includes(v)) Object.assign(body, { state: "closed", state_reason: "not_planned" });
          else throw new TrackerError("other", `"${f.value}" is not a state of a GitHub issue: open, closed or closed as not planned`);
          break;
        }
        case "labels":
          body.labels = splitList(f.value);
          break;
        case "add labels":
          add.push(...splitList(f.value));
          break;
        case "remove labels":
          rm.push(...splitList(f.value));
          break;
        case "assignees":
        case "assignee":
          // An installation is not a user: "me" means nobody here.
          body.assignees = splitList(f.value).filter((a) => !/^(me|none)$/i.test(a));
          break;
        case "milestone":
          body.milestone = await this.milestone(c, r.repo, f.value);
          break;
        case "title":
          body.title = f.value;
          break;
        case "description":
        case "body":
          body.body = f.value;
          break;
        default:
          throw new TrackerError("other", `Unknown field "${f.name}": GitHub Issues sets State, Labels, Add labels, Remove labels, Assignees, Milestone, Title and Description`);
      }
    }
    if (Object.keys(body).length) await c.send("PATCH", path, body);
    if (add.length) await c.send("POST", `${path}/labels`, { labels: add });
    for (const l of rm) {
      await c.send("DELETE", `${path}/labels/${encodeURIComponent(l)}`).catch((err: unknown) => {
        if (!(err instanceof TrackerError && err.kind === "not_found")) throw err;
      });
    }
  }

  private async milestone(c: JsonClient, repo: string, title: string): Promise<number | null> {
    if (!title.trim() || /^none$/i.test(title.trim())) return null;
    const list = (await c.get<{ number: number; title: string }[]>(`repos/${repo}/milestones`, { state: "all", per_page: 100 })) ?? [];
    const m = list.find((x) => x.title.toLowerCase() === title.trim().toLowerCase());
    if (!m) throw new TrackerError("other", `No milestone "${title}" in ${repo}`);
    return m.number;
  }

  async comment(id: string, body: string): Promise<void> {
    const r = parseGitHubId(id);
    await (await this.api()).send("POST", `repos/${r.owner}/${r.name}/issues/${r.number}/comments`, { body });
  }

  async fields(project: string): Promise<Field[]> {
    const labels: Field = { name: "Labels", kind: "labels", multi: true, values: [] };
    const assignees: Field = { name: "Assignees", kind: "user", multi: true, values: [] };
    const ms: Field = { name: "Milestone", kind: "bundle", multi: false, values: [] };
    const m = /^([^/\s]+)\/([^/\s]+)$/.exec(project);
    if (m) {
      const c = await this.api();
      labels.values = ((await c.get<{ name: string }[]>(`repos/${project}/labels`, { per_page: 100 })) ?? []).map((l) => l.name);
      assignees.values = ((await c.get<{ login: string }[]>(`repos/${project}/assignees`, { per_page: 100 }).catch(() => [])) ?? []).map((a) => a.login);
      ms.values = ((await c.get<{ title: string }[]>(`repos/${project}/milestones`, { state: "all", per_page: 100 }).catch(() => [])) ?? []).map((x) => x.title);
    }
    return [{ name: "State", kind: "state", multi: false, values: ["open", "closed", "closed as not planned"] }, labels, { ...labels, name: "Add labels" }, { ...labels, name: "Remove labels" }, assignees, ms];
  }

  /** The repositories the installation sees. */
  async projects(): Promise<ProviderProject[]> {
    const c = await this.api();
    const out: ProviderProject[] = [];
    for (let n = 1; n <= 3; n++) {
      const r = await c.get<{ repositories: { full_name: string; name: string; description?: string | null; archived?: boolean; has_issues?: boolean; owner?: { avatar_url?: string } }[] }>("installation/repositories", { per_page: 100, page: n });
      for (const x of r.repositories ?? []) if (!x.archived && x.has_issues !== false) out.push({ key: x.full_name, name: x.name, description: x.description ?? "", logo: x.owner?.avatar_url ?? "" });
      if ((r.repositories ?? []).length < 100) break;
    }
    return out;
  }
}

export function openGitHub(o: OpenOptions): GitHubIssues {
  if (o.auth !== "vcs") throw new TrackerError("other", `GitHub Issues does not sign in with ${o.auth}`);
  return new GitHubIssues({ signIn: o.vcs ?? vcsSignIn(o.options), allowPrivateNetwork: o.allowPrivateNetwork, retryDelaysMs: o.retryDelaysMs });
}

export const GITHUB: TrackerModule = {
  capabilities: GITHUB_CAPABILITIES,
  open: openGitHub,
};

