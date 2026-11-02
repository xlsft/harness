/**
 * GitHub (github.com and GHES) implementation of VcsProviderClient over REST (plus GraphQL for auto-merge
 * and review thread resolution). Acts with the user's token when given, otherwise with installation tokens
 * of the connection's GitHub App. Every request goes through the SSRF guard.
 */
import { PullRequest_State } from "../gen/harness/agent/v1/agent_pb.ts";
import { CheckState } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import type { ConnectionRow } from "./connections.ts";
import { GH_HEADERS, githubApiBase, githubGraphqlUrl, installationToken, installationTokenForRepo, listInstallations } from "./github-app.ts";
import type { CheckData, CommentData, CreatePrInput, PrData, RepoData, RepoRef, VcsProviderClient } from "./types.ts";
import {
  durationMs,
  httpJson,
  httpRequest,
  isoTime,
  logLimit,
  nextPageFromLink,
  normalizeRepoKey,
  num,
  readTail,
  repoKeyFromUrl,
  str,
  summarize,
  upstreamError,
  type RequestOpts,
} from "./util.ts";

// --- mapping (also used by the webhook handler) --------------------------------------------------

export function githubPrState(pr: any): PullRequest_State {
  if (pr.merged || pr.merged_at) return PullRequest_State.MERGED;
  if (pr.state === "closed") return PullRequest_State.CLOSED;
  return pr.draft ? PullRequest_State.DRAFT : PullRequest_State.OPEN;
}

/** Normalized PR from a REST / webhook pull request object. reviewState and checks are filled by the caller. */
export function githubPr(pr: any, repoKey?: string): PrData {
  const key = repoKey ?? (pr.base?.repo?.html_url ? repoKeyFromUrl(pr.base.repo.html_url) : repoKeyFromUrl(String(pr.html_url).replace(/\/pull\/\d+$/, "")));
  return {
    repositoryKey: key,
    number: num(pr.number),
    title: str(pr.title),
    url: str(pr.html_url),
    headBranch: str(pr.head?.ref),
    baseBranch: str(pr.base?.ref),
    author: str(pr.user?.login),
    state: githubPrState(pr),
    reviewState: (pr.requested_reviewers?.length ?? 0) > 0 ? "review_required" : "none",
    mergeable: pr.mergeable === true,
    hasConflicts: pr.mergeable_state === "dirty" || pr.mergeable === false,
    autoMergeEnabled: !!pr.auto_merge,
    draft: !!pr.draft,
    updatedAt: isoTime(pr.updated_at),
    headSha: str(pr.head?.sha),
    nodeId: str(pr.node_id),
  };
}

export function githubCheckRun(cr: any): CheckData {
  let state: CheckState;
  if (cr.status !== "completed") state = cr.status === "in_progress" ? CheckState.RUNNING : CheckState.QUEUED;
  else {
    switch (cr.conclusion) {
      case "success":
        state = CheckState.SUCCESS;
        break;
      case "failure":
      case "timed_out":
      case "startup_failure":
      case "action_required":
        state = CheckState.FAILURE;
        break;
      case "cancelled":
        state = CheckState.CANCELLED;
        break;
      case "skipped":
        state = CheckState.SKIPPED;
        break;
      default:
        state = CheckState.NEUTRAL;
    }
  }
  const summary = [str(cr.output?.title), str(cr.output?.summary)].filter(Boolean).join(": ");
  return {
    id: `cr:${cr.id}`,
    name: str(cr.name),
    state,
    url: str(cr.html_url || cr.details_url),
    workflow: str(cr.check_suite?.app?.name || cr.app?.name),
    durationMs: durationMs(cr.started_at, cr.status === "completed" ? cr.completed_at : undefined),
    summary: summary.slice(0, 1000),
  };
}

export function githubStatus(s: any): CheckData {
  const state =
    s.state === "success" ? CheckState.SUCCESS : s.state === "failure" || s.state === "error" ? CheckState.FAILURE : CheckState.RUNNING;
  return {
    id: `st:${s.id}`,
    name: str(s.context),
    state,
    url: str(s.target_url),
    workflow: "status",
    durationMs: 0,
    summary: str(s.description).slice(0, 1000),
  };
}

const MERGE_METHODS: Record<string, string> = { merge: "MERGE", squash: "SQUASH", rebase: "REBASE" };

function mergeMethod(m: string): string {
  const v = (m || "merge").toLowerCase();
  if (!MERGE_METHODS[v]) throw new ApiError("invalid_request", "method must be merge, squash or rebase");
  return v;
}

/** Splits our comment ids: "r:<id>" review comment, "c:<id>" conversation comment (a bare number = review). */
function commentRef(id: string): { kind: "r" | "c"; id: string } {
  const m = /^(r|c):(\d+)$/.exec(id) ?? /^()(\d+)$/.exec(id);
  if (!m) throw new ApiError("invalid_request", `Invalid comment id ${id}`);
  return { kind: (m[1] || "r") as "r" | "c", id: m[2]! };
}

export interface GitHubAuth {
  /** The user's OAuth token; undefined acts with installation tokens. */
  userToken?: string;
}

export class GitHubClient implements VcsProviderClient {
  private readonly api: string;

  constructor(
    private readonly conn: ConnectionRow,
    private readonly auth: GitHubAuth,
  ) {
    this.api = githubApiBase(conn.baseUrl);
  }

  private async authz(repo?: RepoRef): Promise<string> {
    if (this.auth.userToken) return `Bearer ${this.auth.userToken}`;
    if (!repo) throw new ApiError("needs_auth", "Connect your GitHub account first");
    return `Bearer ${await installationTokenForRepo(this.conn, repo.owner, repo.name)}`;
  }

  private repoPath(repo: RepoRef): string {
    return `${this.api}/repos/${repo.owner}/${repo.name}`;
  }

  private async json<T>(url: string, what: string, repo: RepoRef | undefined, o: RequestOpts = {}) {
    return httpJson<T>(this.conn, url, what, { ...o, headers: { ...GH_HEADERS, ...o.headers }, authorization: await this.authz(repo), userToken: !!this.auth.userToken });
  }

  private async graphql<T>(query: string, variables: Record<string, unknown>, repo: RepoRef): Promise<T> {
    const { data } = await httpJson<{ data?: T; errors?: { message: string }[] }>(this.conn, githubGraphqlUrl(this.conn.baseUrl), "GitHub GraphQL", {
      method: "POST",
      headers: GH_HEADERS,
      body: { query, variables },
      authorization: await this.authz(repo),
      userToken: !!this.auth.userToken,
    });
    if (data.errors?.length) throw new ApiError("upstream", `GitHub GraphQL: ${data.errors.map((e) => e.message).join("; ").slice(0, 500)}`);
    return data.data as T;
  }

  async whoami(): Promise<string> {
    if (!this.auth.userToken) return "";
    const { data } = await this.json<any>(`${this.api}/user`, "Get the GitHub user", undefined);
    return str(data?.login);
  }

  async canAccess(repo: RepoRef): Promise<boolean> {
    try {
      await this.json<any>(this.repoPath(repo), "Get the repository", repo);
      return true;
    } catch (err) {
      if (err instanceof ApiError && ["not_found", "permission", "failed_precondition"].includes(err.type)) return false;
      throw err;
    }
  }

  private repoData(r: any): RepoData {
    return {
      connectionId: this.conn.id,
      key: repoKeyFromUrl(str(r.html_url)),
      fullName: str(r.full_name),
      defaultBranch: str(r.default_branch),
      url: str(r.html_url),
      private: !!r.private,
    };
  }

  async listRepositories(query: string, pageToken: string) {
    const q = query.trim().toLowerCase();
    const match = (r: RepoData) => !q || r.fullName.toLowerCase().includes(q);
    if (this.auth.userToken) {
      const page = Number(pageToken || "1") || 1;
      const { data, res } = await this.json<any[]>(`${this.api}/user/repos?per_page=100&sort=pushed&page=${page}`, "List repositories", undefined);
      return { repositories: (data ?? []).map((r) => this.repoData(r)).filter(match), nextPageToken: nextPageFromLink(res.headers.get("link")) };
    }
    // Installation repositories: page token "<installation index>:<page>".
    const installs = await listInstallations(this.conn);
    let [idx, page] = (pageToken || "0:1").split(":").map(Number) as [number, number];
    if (!Number.isFinite(idx) || !Number.isFinite(page)) throw new ApiError("invalid_request", "Invalid page_token");
    const inst = installs[idx];
    if (!inst) return { repositories: [], nextPageToken: "" };
    const token = await installationToken(this.conn, inst.id);
    const { data } = await httpJson<{ repositories: any[] }>(this.conn, `${this.api}/installation/repositories?per_page=100&page=${page}`, "List installation repositories", {
      headers: GH_HEADERS,
      authorization: `Bearer ${token}`,
    });
    const list = data?.repositories ?? [];
    const next = list.length === 100 ? `${idx}:${page + 1}` : idx + 1 < installs.length ? `${idx + 1}:1` : "";
    return { repositories: list.map((r) => this.repoData(r)).filter(match), nextPageToken: next };
  }

  private async reviewState(repo: RepoRef, pr: any): Promise<string> {
    const { data } = await this.json<any[]>(`${this.repoPath(repo)}/pulls/${pr.number}/reviews?per_page=100`, "List reviews", repo);
    const latest: Record<string, string> = {};
    for (const r of data ?? []) {
      const s = str(r.state).toLowerCase();
      if (s === "approved" || s === "changes_requested" || s === "dismissed") latest[str(r.user?.login)] = s;
    }
    const states = Object.values(latest);
    if (states.includes("changes_requested")) return "changes_requested";
    if (states.includes("approved")) return "approved";
    return (pr.requested_reviewers?.length ?? 0) > 0 || (pr.requested_teams?.length ?? 0) > 0 ? "review_required" : "none";
  }

  /** Full PR: details, review state and checks of the head commit. */
  private async enrich(repo: RepoRef, raw: any): Promise<PrData> {
    const pr = githubPr(raw, repo.key);
    const [reviewState, checks] = await Promise.all([
      this.reviewState(repo, raw).catch(() => pr.reviewState),
      pr.headSha ? this.checksForSha(repo, pr.headSha).catch(() => undefined) : Promise.resolve(undefined),
    ]);
    pr.reviewState = reviewState;
    if (checks) pr.checks = summarize(checks);
    return pr;
  }

  async listPullRequests(repo: RepoRef, opts: { headBranch: string; includeClosed: boolean }): Promise<PrData[]> {
    const params = new URLSearchParams({ state: opts.includeClosed ? "all" : "open", per_page: "50", sort: "updated", direction: "desc" });
    if (opts.headBranch) params.set("head", `${repo.owner}:${opts.headBranch}`);
    const { data } = await this.json<any[]>(`${this.repoPath(repo)}/pulls?${params}`, "List pull requests", repo);
    const list = data ?? [];
    // Enrich small result sets (the usual "PR for this branch" query) with review state and checks.
    if (list.length <= 10) return Promise.all(list.map((p) => this.getPullRequest(repo, num(p.number))));
    return list.map((p) => githubPr(p, repo.key));
  }

  async getPullRequest(repo: RepoRef, number: number): Promise<PrData> {
    const { data } = await this.json<any>(`${this.repoPath(repo)}/pulls/${number}`, "Get the pull request", repo);
    return this.enrich(repo, data);
  }

  async createPullRequest(repo: RepoRef, input: CreatePrInput): Promise<PrData> {
    const { data } = await this.json<any>(`${this.repoPath(repo)}/pulls`, "Create the pull request", repo, {
      method: "POST",
      body: { title: input.title, head: input.headBranch, base: input.baseBranch, body: input.body, draft: input.draft },
    });
    return this.enrich(repo, data);
  }

  async mergePullRequest(repo: RepoRef, number: number, method: string): Promise<PrData> {
    await this.json<any>(`${this.repoPath(repo)}/pulls/${number}/merge`, "Merge the pull request", repo, {
      method: "PUT",
      body: { merge_method: mergeMethod(method) },
    });
    return this.getPullRequest(repo, number);
  }

  async setAutoMerge(repo: RepoRef, number: number, enabled: boolean, method: string): Promise<void> {
    const { data } = await this.json<any>(`${this.repoPath(repo)}/pulls/${number}`, "Get the pull request", repo);
    const id = str(data?.node_id);
    if (!id) throw new ApiError("upstream", "The pull request has no node id");
    if (enabled) {
      await this.graphql(
        `mutation($id: ID!, $method: PullRequestMergeMethod!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $method}) { clientMutationId } }`,
        { id, method: MERGE_METHODS[mergeMethod(method)] },
        repo,
      );
    } else {
      await this.graphql(`mutation($id: ID!) { disablePullRequestAutoMerge(input: {pullRequestId: $id}) { clientMutationId } }`, { id }, repo);
    }
  }

  private reviewComment(c: any, resolved: boolean): CommentData {
    return {
      id: `r:${c.id}`,
      author: str(c.user?.login),
      body: str(c.body),
      path: str(c.path),
      line: num(c.line ?? c.original_line),
      resolved,
      url: str(c.html_url),
      createdAt: isoTime(c.created_at),
    };
  }

  private issueComment(c: any): CommentData {
    return { id: `c:${c.id}`, author: str(c.user?.login), body: str(c.body), path: "", line: 0, resolved: false, url: str(c.html_url), createdAt: isoTime(c.created_at) };
  }

  /** Review comment database ids that belong to resolved threads (best effort). */
  private async resolvedIds(repo: RepoRef, number: number): Promise<Set<number>> {
    const out = new Set<number>();
    try {
      const d = await this.graphql<any>(
        `query($owner: String!, $name: String!, $number: Int!) { repository(owner: $owner, name: $name) { pullRequest(number: $number) { reviewThreads(first: 100) { nodes { isResolved comments(first: 100) { nodes { databaseId } } } } } } }`,
        { owner: repo.owner, name: repo.name, number },
        repo,
      );
      for (const t of d?.repository?.pullRequest?.reviewThreads?.nodes ?? []) {
        if (t.isResolved) for (const c of t.comments?.nodes ?? []) out.add(num(c.databaseId));
      }
    } catch {
      // Resolution state is optional.
    }
    return out;
  }

  async listReviewComments(repo: RepoRef, number: number, unresolvedOnly: boolean): Promise<CommentData[]> {
    const [review, issue, resolved] = await Promise.all([
      this.json<any[]>(`${this.repoPath(repo)}/pulls/${number}/comments?per_page=100`, "List review comments", repo),
      this.json<any[]>(`${this.repoPath(repo)}/issues/${number}/comments?per_page=100`, "List comments", repo),
      this.resolvedIds(repo, number),
    ]);
    const out = [
      ...(review.data ?? []).map((c) => this.reviewComment(c, resolved.has(num(c.id)))),
      ...(issue.data ?? []).map((c) => this.issueComment(c)),
    ];
    out.sort((a, b) => (a.createdAt < b.createdAt ? -1 : a.createdAt > b.createdAt ? 1 : 0));
    return unresolvedOnly ? out.filter((c) => !c.resolved) : out;
  }

  async postComment(repo: RepoRef, number: number, body: string, inReplyTo: string): Promise<CommentData> {
    if (!body.trim()) throw new ApiError("invalid_request", "body is required");
    if (!inReplyTo) {
      const { data } = await this.json<any>(`${this.repoPath(repo)}/issues/${number}/comments`, "Post the comment", repo, { method: "POST", body: { body } });
      return this.issueComment(data);
    }
    const ref = commentRef(inReplyTo);
    if (ref.kind === "c") {
      // Conversation comments have no threads; reply in the conversation.
      const { data } = await this.json<any>(`${this.repoPath(repo)}/issues/${number}/comments`, "Post the comment", repo, { method: "POST", body: { body } });
      return this.issueComment(data);
    }
    const { data } = await this.json<any>(`${this.repoPath(repo)}/pulls/${number}/comments/${ref.id}/replies`, "Reply to the review comment", repo, {
      method: "POST",
      body: { body },
    });
    return this.reviewComment(data, false);
  }

  private async checksForSha(repo: RepoRef, sha: string): Promise<CheckData[]> {
    const [runs, status] = await Promise.all([
      this.json<{ check_runs: any[] }>(`${this.repoPath(repo)}/commits/${sha}/check-runs?per_page=100&filter=latest`, "List check runs", repo),
      this.json<{ statuses: any[] }>(`${this.repoPath(repo)}/commits/${sha}/status?per_page=100`, "Get the combined status", repo),
    ]);
    return [...(runs.data?.check_runs ?? []).map(githubCheckRun), ...(status.data?.statuses ?? []).map(githubStatus)];
  }

  async listChecks(repo: RepoRef, sel: { number?: number; sha?: string }) {
    let sha = sel.sha ?? "";
    if (!sha) {
      if (!sel.number) throw new ApiError("invalid_request", "number or sha is required");
      const { data } = await this.json<any>(`${this.repoPath(repo)}/pulls/${sel.number}`, "Get the pull request", repo);
      sha = str(data?.head?.sha);
    }
    const checks = await this.checksForSha(repo, sha);
    return { checks, summary: summarize(checks), sha };
  }

  async getCheckLog(repo: RepoRef, checkId: string, maxBytes: number) {
    const m = /^(cr:)?(\d+)$/.exec(checkId);
    if (!m) {
      if (checkId.startsWith("st:")) throw new ApiError("invalid_request", "Commit statuses have no log; open the check URL instead");
      throw new ApiError("invalid_request", `Invalid check id ${checkId}`);
    }
    const id = m[2]!;
    // GitHub Actions: the job id equals the check run id. The endpoint redirects to a short-lived log URL on
    // another host; that hop is fetched through the SSRF guard without our credentials.
    const res = await httpRequest(this.conn, `${this.repoPath(repo)}/actions/jobs/${id}/logs`, {
      headers: GH_HEADERS,
      authorization: await this.authz(repo),
      followRedirects: false,
    });
    let logRes = res;
    if (res.status >= 300 && res.status < 400) {
      const location = res.headers.get("location");
      await res.body?.cancel();
      if (!location) throw new ApiError("upstream", "The log redirect has no location");
      logRes = await httpRequest(this.conn, new URL(location, res.url || this.api).href, { timeoutMs: 60_000 });
    }
    if (!logRes.ok) {
      const err = await upstreamError(logRes, "Download the job log", !!this.auth.userToken);
      if (err.type === "not_found") throw new ApiError("not_found", "No log for this check (only GitHub Actions jobs have logs)");
      throw err;
    }
    return readTail(logRes, logLimit(maxBytes));
  }
}

/** Repository key of a webhook payload's repository object. */
export function githubRepoKey(repository: any): string {
  if (repository?.html_url) return repoKeyFromUrl(repository.html_url);
  return normalizeRepoKey("github.com", str(repository?.full_name));
}
