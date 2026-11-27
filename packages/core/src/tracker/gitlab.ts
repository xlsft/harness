/**
 * GitLab Issues for company connections. It signs in through a code host connection from Settings > VCS (the "vcs" method), with
 * that connection's access token; comments and changes are its account's. The client's packages/client/tracker/gitlab does the
 * same with the glab login. REST API: https://docs.gitlab.com/api/issues/, https://docs.gitlab.com/api/notes/.
 * docs/design/tracker.md, "Writing a provider".
 */
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

export const GITLAB_CAPABILITIES: Capabilities = {
  provider: "gitlab",
  displayName: "GitLab Issues",
  authMethods: ["vcs"],
  queryLanguage: "GitLab filters",
  queryExample: "project:group/app state:opened label:bug assignee:me login page",
  customFields: false,
  comments: true,
  optionFields: VCS_OPTION_FIELDS.filter((f) => f.key === "vcs_connection_id"),
  docsUrl: "https://docs.gitlab.com/api/issues/",
  logo: "gitlab",
  baseUrlPlaceholder: "https://gitlab.com",
  defaultBaseUrl: "https://gitlab.com",
  authHints: { vcs: { help: "Signs in through GitLab: on this device the glab command line (glab auth login), for the company a connection from Settings → VCS." } },
};

/** Splits group/project#12 (groups nest, so the last # ends the path). */
export function parseGitLabId(id: string): { project: string; iid: number } {
  const t = id.trim();
  const i = t.lastIndexOf("#");
  const project = i > 0 ? t.slice(0, i) : "";
  const iid = i > 0 ? Number(t.slice(i + 1)) : NaN;
  if (!project.includes("/") || /\s/.test(project) || !Number.isInteger(iid) || iid <= 0) throw new TrackerError("not_found", `"${id}" is not group/project#number`);
  return { project, iid };
}

const projectPath = (project: string) => `projects/${encodeURIComponent(project)}`;

export interface GitLabFilter {
  path: string;
  params: Record<string, string>;
}

/** Splits on spaces outside quotes. */
function tokens(q: string): string[] {
  return q.match(/(?:[^\s"]+|"[^"]*")+/g) ?? [];
}

/**
 * GitLab filters: project:, group:, state:, label: (repeatable), assignee:, author:, milestone:, scope:, and free text, which
 * searches titles and descriptions. Without project: or group: the search covers the issues the account can see (scope all).
 */
export function parseGitLabQuery(q: string): GitLabFilter {
  const f: GitLabFilter = { path: "issues", params: {} };
  const text: string[] = [];
  const labels: string[] = [];
  for (const tok of tokens(q)) {
    const i = tok.indexOf(":");
    const key = i > 0 ? tok.slice(0, i) : "";
    const val = i > 0 ? tok.slice(i + 1).replace(/^"|"$/g, "") : "";
    if (!key || !val || /[\s"]/.test(key)) {
      text.push(tok.replace(/^"|"$/g, ""));
      continue;
    }
    switch (key.toLowerCase()) {
      case "project":
      case "repo":
        f.path = `${projectPath(val)}/issues`;
        break;
      case "group":
        f.path = `groups/${encodeURIComponent(val)}/issues`;
        break;
      case "state":
      case "is": {
        const v = val.toLowerCase();
        if (v === "open" || v === "opened") f.params.state = "opened";
        else if (v === "closed") f.params.state = "closed";
        else if (v !== "all") throw new TrackerError("other", `state:${val}: GitLab issues are opened or closed`);
        break;
      }
      case "label":
      case "labels":
        labels.push(val);
        break;
      case "assignee":
        f.params.assignee_username = val;
        break;
      case "author":
        f.params.author_username = val;
        break;
      case "milestone":
        f.params.milestone = val;
        break;
      case "scope":
        f.params.scope = val;
        break;
      default:
        throw new TrackerError("other", `Unknown filter "${key}": GitLab filters are project, group, state, label, assignee, author, milestone and scope`);
    }
  }
  if (labels.length) f.params.labels = labels.join(",");
  if (text.join(" ").trim()) f.params.search = text.join(" ").trim();
  if (f.path === "issues" && !f.params.scope) f.params.scope = "all";
  return f;
}

interface ApiUser {
  id: number;
  username: string;
  name?: string;
}

interface ApiIssue {
  iid: number;
  title: string;
  description?: string | null;
  web_url: string;
  state: string;
  updated_at?: string;
  labels?: string[];
  assignees?: ApiUser[];
  milestone?: { title: string } | null;
  references?: { full?: string };
}

function toIssue(i: ApiIssue, project?: string): Issue {
  const full = i.references?.full ?? "";
  const p = project || (full.lastIndexOf("#") > 0 ? full.slice(0, full.lastIndexOf("#")) : "");
  const fields: { name: string; value: string }[] = [];
  const add = (name: string, v: string | undefined) => v && fields.push({ name, value: v });
  add("State", i.state);
  add("Labels", (i.labels ?? []).join(", "));
  add("Assignees", (i.assignees ?? []).map((a) => a.username).join(", "));
  add("Milestone", i.milestone?.title);
  return { id: `${p}#${i.iid}`, title: i.title, url: i.web_url, description: i.description ?? "", project: p, fields, updatedAt: i.updated_at ?? "", comments: [] };
}

export interface GitLabOptions {
  signIn: () => Promise<VcsSignIn>;
  allowPrivateNetwork: boolean;
  retryDelaysMs?: number[];
}

export class GitLabIssues implements TrackerProvider {
  private client?: Promise<JsonClient>;
  private me?: ApiUser;

  constructor(private readonly o: GitLabOptions) {}

  capabilities(): Capabilities {
    return GITLAB_CAPABILITIES;
  }

  private async api(): Promise<JsonClient> {
    this.client ??= this.o
      .signIn()
      .catch(vcsTrackerError)
      .then((v) => {
        if (v.type !== "gitlab") throw new TrackerError("other", "GitLab Issues signs in through a GitLab connection, not a GitHub one");
        return new JsonClient({
          base: `${v.baseUrl.replace(/\/+$/, "")}/api/v4`,
          name: "GitLab",
          allowPrivateNetwork: this.o.allowPrivateNetwork,
          retryDelaysMs: this.o.retryDelaysMs,
          authorize: async () => ({ authorization: `Bearer ${await v.token().catch(vcsTrackerError)}` }),
        });
      });
    return this.client.catch((err: unknown) => {
      this.client = undefined;
      throw err;
    });
  }

  private async user(): Promise<ApiUser> {
    this.me ??= await (await this.api()).get<ApiUser>("user");
    return this.me;
  }

  async test(): Promise<Identity> {
    const me = await this.user();
    return { login: me.username, displayName: me.name || me.username };
  }

  async search(query: string, page: Page): Promise<{ issues: Issue[]; hasMore: boolean }> {
    const f = parseGitLabQuery(query);
    for (const k of ["assignee_username", "author_username"]) if (f.params[k]?.toLowerCase() === "me") f.params[k] = (await this.user()).username;
    const c = await this.api();
    const limit = Math.max(page.limit, 1);
    const per = 100;
    let skip = page.offset ?? 0;
    const out: Issue[] = [];
    for (let n = Math.floor(skip / per) + 1, first = true; n <= 10; n++, first = false) {
      if (first) skip %= per;
      const list = (await c.get<ApiIssue[]>(f.path, { ...f.params, per_page: per, page: n, order_by: "updated_at" })) ?? [];
      for (const i of list) {
        if (skip > 0) {
          skip--;
          continue;
        }
        if (out.length === limit) return { issues: out, hasMore: true };
        out.push(toIssue(i));
      }
      if (list.length < per) break;
    }
    return { issues: out, hasMore: false };
  }

  async get(id: string): Promise<Issue> {
    const { project, iid } = parseGitLabId(id);
    const c = await this.api();
    const path = `${projectPath(project)}/issues/${iid}`;
    const issue = toIssue(await c.get<ApiIssue>(path), project);
    const notes = (await c.get<{ body: string; created_at: string; system?: boolean; author?: ApiUser }[]>(`${path}/notes`, { per_page: 100, sort: "desc", order_by: "created_at" })) ?? [];
    issue.comments = notes
      .filter((n) => !n.system)
      .reverse()
      .map((n) => ({ author: n.author?.username ?? "", text: n.body, createdAt: n.created_at }))
      .slice(-10);
    return issue;
  }

  async create(d: IssueDraft): Promise<Issue> {
    if (!d.project.includes("/")) throw new TrackerError("other", `"${d.project}" is not group/project`);
    const c = await this.api();
    const created = toIssue(await c.send<ApiIssue>("POST", `${projectPath(d.project)}/issues`, { title: d.title, description: d.description ?? "" }), d.project);
    if (d.fields?.length) await this.update(created.id, { fields: d.fields });
    return created;
  }

  private async userId(name: string): Promise<number> {
    if (/^me$/i.test(name)) return (await this.user()).id;
    const list = (await (await this.api()).get<ApiUser[]>("users", { username: name })) ?? [];
    if (!list[0]) throw new TrackerError("other", `No GitLab user "${name}"`);
    return list[0].id;
  }

  private async milestone(project: string, title: string): Promise<number> {
    const t = title.trim();
    if (!t || /^none$/i.test(t)) return 0;
    const list = (await (await this.api()).get<{ id: number; title: string }[]>(`${projectPath(project)}/milestones`, { per_page: 100 })) ?? [];
    const m = list.find((x) => x.title.toLowerCase() === t.toLowerCase());
    if (!m) throw new TrackerError("other", `No milestone "${title}" in ${project}`);
    return m.id;
  }

  async update(id: string, u: IssueUpdate): Promise<void> {
    const { project, iid } = parseGitLabId(id);
    const body: Record<string, unknown> = {};
    if (u.title !== undefined) body.title = u.title;
    if (u.description !== undefined) body.description = u.description;
    for (const f of u.fields ?? []) {
      switch (f.name.trim().toLowerCase()) {
        case "state":
        case "status":
        case "stage": {
          const v = f.value.trim().toLowerCase();
          if (["open", "opened", "reopen", "reopened"].includes(v)) body.state_event = "reopen";
          else if (["closed", "close", "done", "completed"].includes(v)) body.state_event = "close";
          else throw new TrackerError("other", `"${f.value}" is not a state of a GitLab issue: opened or closed`);
          break;
        }
        case "labels":
          body.labels = splitList(f.value).join(",");
          break;
        case "add labels":
          body.add_labels = splitList(f.value).join(",");
          break;
        case "remove labels":
          body.remove_labels = splitList(f.value).join(",");
          break;
        case "assignees":
        case "assignee": {
          const ids: number[] = [];
          for (const a of splitList(f.value)) if (!/^none$/i.test(a)) ids.push(await this.userId(a));
          body.assignee_ids = ids.length ? ids : [0];
          break;
        }
        case "milestone":
          body.milestone_id = await this.milestone(project, f.value);
          break;
        case "title":
          body.title = f.value;
          break;
        case "description":
        case "body":
          body.description = f.value;
          break;
        default:
          throw new TrackerError("other", `Unknown field "${f.name}": GitLab Issues sets State, Labels, Add labels, Remove labels, Assignees, Milestone, Title and Description`);
      }
    }
    if (Object.keys(body).length) await (await this.api()).send("PUT", `${projectPath(project)}/issues/${iid}`, body);
  }

  async comment(id: string, body: string): Promise<void> {
    const { project, iid } = parseGitLabId(id);
    await (await this.api()).send("POST", `${projectPath(project)}/issues/${iid}/notes`, { body });
  }

  async fields(project: string): Promise<Field[]> {
    const labels: Field = { name: "Labels", kind: "labels", multi: true, values: [] };
    const assignees: Field = { name: "Assignees", kind: "user", multi: true, values: [] };
    const ms: Field = { name: "Milestone", kind: "bundle", multi: false, values: [] };
    if (project.includes("/")) {
      const c = await this.api();
      const p = projectPath(project);
      labels.values = ((await c.get<{ name: string }[]>(`${p}/labels`, { per_page: 100 })) ?? []).map((l) => l.name);
      assignees.values = ((await c.get<ApiUser[]>(`${p}/members/all`, { per_page: 100 }).catch(() => [])) ?? []).map((a) => a.username);
      ms.values = ((await c.get<{ title: string }[]>(`${p}/milestones`, { per_page: 100 }).catch(() => [])) ?? []).map((m) => m.title);
    }
    return [{ name: "State", kind: "state", multi: false, values: ["opened", "closed"] }, labels, { ...labels, name: "Add labels" }, { ...labels, name: "Remove labels" }, assignees, ms];
  }

  /** The projects the account is a member of with issues on, most recently active first. */
  async projects(): Promise<ProviderProject[]> {
    const c = await this.api();
    const out: ProviderProject[] = [];
    for (let n = 1; n <= 3; n++) {
      const list = (await c.get<{ path_with_namespace: string; name: string; description?: string | null; avatar_url?: string | null }[]>("projects", {
        membership: "true",
        archived: "false",
        with_issues_enabled: "true",
        order_by: "last_activity_at",
        simple: "true",
        per_page: 100,
        page: n,
      })) ?? [];
      for (const x of list) out.push({ key: x.path_with_namespace, name: x.name, description: x.description ?? "", logo: x.avatar_url ?? "" });
      if (list.length < 100) break;
    }
    return out;
  }
}


export function openGitLab(o: OpenOptions): GitLabIssues {
  if (o.auth !== "vcs") throw new TrackerError("other", `GitLab Issues does not sign in with ${o.auth}`);
  return new GitLabIssues({ signIn: o.vcs ?? vcsSignIn(o.options), allowPrivateNetwork: o.allowPrivateNetwork, retryDelaysMs: o.retryDelaysMs });
}

export const GITLAB: TrackerModule = {
  capabilities: GITLAB_CAPABILITIES,
  open: openGitLab,
};
