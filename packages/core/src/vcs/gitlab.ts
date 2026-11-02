/**
 * GitLab (gitlab.com and self-managed) implementation of VcsProviderClient over the REST API v4. Acts with
 * the user's OAuth token, or with the connection's group/project access token. Merge requests are the
 * pull requests (number = iid); pipelines and jobs are the checks.
 */
import { PullRequest_State } from "../gen/harness/agent/v1/agent_pb.ts";
import { CheckState } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { ApiError } from "../lib/errors.ts";
import type { ConnectionRow } from "./connections.ts";
import type { CheckData, CommentData, CreatePrInput, PrData, RepoData, RepoRef, VcsProviderClient } from "./types.ts";
import { httpJson, httpRequest, isoTime, logLimit, num, readTail, repoKeyFromUrl, str, summarize, upstreamError, type RequestOpts } from "./util.ts";

export function gitlabApiBase(baseUrl: string): string {
  return `${baseUrl}/api/v4`;
}

export function gitlabMrState(mr: any): PullRequest_State {
  const s = str(mr.state);
  if (s === "merged") return PullRequest_State.MERGED;
  if (s === "closed" || s === "locked") return PullRequest_State.CLOSED;
  return mr.draft || mr.work_in_progress ? PullRequest_State.DRAFT : PullRequest_State.OPEN;
}

/** Normalized MR from a REST merge request or a webhook's object_attributes. */
export function gitlabMr(mr: any, repoKey: string): PrData {
  const detailed = str(mr.detailed_merge_status);
  const status = str(mr.merge_status);
  return {
    repositoryKey: repoKey,
    number: num(mr.iid),
    title: str(mr.title),
    url: str(mr.web_url ?? mr.url),
    headBranch: str(mr.source_branch),
    baseBranch: str(mr.target_branch),
    author: str(mr.author?.username),
    state: gitlabMrState(mr),
    reviewState: detailed === "requested_changes" ? "changes_requested" : detailed === "not_approved" ? "review_required" : "none",
    mergeable: detailed === "mergeable" || (!detailed && status === "can_be_merged"),
    hasConflicts: !!mr.has_conflicts || detailed === "conflict" || status === "cannot_be_merged",
    autoMergeEnabled: !!(mr.merge_when_pipeline_succeeds ?? mr.auto_merge_enabled),
    draft: !!(mr.draft || mr.work_in_progress),
    updatedAt: isoTime(mr.updated_at),
    headSha: str(mr.sha ?? mr.last_commit?.id),
  };
}

export function gitlabJobState(status: string, allowFailure: boolean): CheckState {
  switch (status) {
    case "success":
      return CheckState.SUCCESS;
    case "failed":
      return allowFailure ? CheckState.NEUTRAL : CheckState.FAILURE;
    case "canceled":
    case "canceling":
      return CheckState.CANCELLED;
    case "skipped":
      return CheckState.SKIPPED;
    case "manual":
      return CheckState.NEUTRAL;
    case "running":
      return CheckState.RUNNING;
    default:
      // created, pending, preparing, waiting_for_resource, scheduled
      return CheckState.QUEUED;
  }
}

export function gitlabJob(j: any, pipelineName = ""): CheckData {
  const stage = str(j.stage);
  return {
    id: `job:${j.id}`,
    name: str(j.name),
    state: gitlabJobState(str(j.status), !!j.allow_failure),
    url: str(j.web_url),
    workflow: [pipelineName, stage].filter(Boolean).join(" / "),
    durationMs: Math.round(num(j.duration) * 1000),
    summary: str(j.failure_reason),
  };
}

const project = (repo: RepoRef) => encodeURIComponent(repo.path);

export interface GitLabAuth {
  /** The user's OAuth token, or the connection's access token. */
  token: string;
  userToken: boolean;
}

export class GitLabClient implements VcsProviderClient {
  private readonly api: string;

  constructor(
    private readonly conn: ConnectionRow,
    private readonly auth: GitLabAuth,
  ) {
    this.api = gitlabApiBase(conn.baseUrl);
  }

  private json<T>(url: string, what: string, o: RequestOpts = {}) {
    return httpJson<T>(this.conn, url, what, { ...o, authorization: `Bearer ${this.auth.token}`, userToken: this.auth.userToken });
  }

  private mrPath(repo: RepoRef, iid?: number): string {
    return `${this.api}/projects/${project(repo)}/merge_requests${iid ? `/${iid}` : ""}`;
  }

  async whoami(): Promise<string> {
    const { data } = await this.json<any>(`${this.api}/user`, "Get the GitLab user");
    return str(data?.username);
  }

  async canAccess(repo: RepoRef): Promise<boolean> {
    try {
      await this.json<any>(`${this.api}/projects/${project(repo)}`, "Get the project");
      return true;
    } catch (err) {
      if (err instanceof ApiError && (err.type === "not_found" || err.type === "permission")) return false;
      throw err;
    }
  }

  async listRepositories(query: string, pageToken: string) {
    const page = Number(pageToken || "1") || 1;
    const params = new URLSearchParams({ membership: "true", simple: "true", per_page: "100", page: String(page), order_by: "last_activity_at" });
    if (query.trim()) params.set("search", query.trim());
    const { data, res } = await this.json<any[]>(`${this.api}/projects?${params}`, "List projects");
    const repositories: RepoData[] = (data ?? []).map((p) => ({
      connectionId: this.conn.id,
      key: repoKeyFromUrl(str(p.web_url)),
      fullName: str(p.path_with_namespace),
      defaultBranch: str(p.default_branch),
      url: str(p.web_url),
      private: str(p.visibility) !== "public",
    }));
    return { repositories, nextPageToken: str(res.headers.get("x-next-page")) };
  }

  private async approvalsState(repo: RepoRef, iid: number, current: string): Promise<string> {
    if (current === "changes_requested") return current;
    try {
      const { data } = await this.json<any>(`${this.mrPath(repo, iid)}/approvals`, "Get approvals");
      if (data?.approved && (data.approvals_left ?? 0) === 0 && (data.approved_by?.length ?? 0) > 0) return "approved";
      if ((data?.approvals_left ?? 0) > 0) return "review_required";
    } catch {
      // approvals are optional (CE without approval rules)
    }
    return current;
  }

  private async pipelineJobs(repo: RepoRef, pipelineId: number): Promise<CheckData[]> {
    const { data } = await this.json<any[]>(`${this.api}/projects/${project(repo)}/pipelines/${pipelineId}/jobs?per_page=100`, "List pipeline jobs");
    return (data ?? []).map((j) => gitlabJob(j));
  }

  private async enrich(repo: RepoRef, raw: any): Promise<PrData> {
    const pr = gitlabMr(raw, repo.key);
    pr.reviewState = await this.approvalsState(repo, pr.number, pr.reviewState);
    const pipelineId = num(raw.head_pipeline?.id ?? raw.pipeline?.id);
    if (pipelineId) {
      const jobs = await this.pipelineJobs(repo, pipelineId).catch(() => undefined);
      if (jobs) pr.checks = summarize(jobs);
    }
    return pr;
  }

  async listPullRequests(repo: RepoRef, opts: { headBranch: string; includeClosed: boolean }): Promise<PrData[]> {
    const params = new URLSearchParams({ state: opts.includeClosed ? "all" : "opened", per_page: "50", order_by: "updated_at" });
    if (opts.headBranch) params.set("source_branch", opts.headBranch);
    const { data } = await this.json<any[]>(`${this.mrPath(repo)}?${params}`, "List merge requests");
    const list = data ?? [];
    if (list.length <= 10) return Promise.all(list.map((m) => this.getPullRequest(repo, num(m.iid))));
    return list.map((m) => gitlabMr(m, repo.key));
  }

  async getPullRequest(repo: RepoRef, number: number): Promise<PrData> {
    const { data } = await this.json<any>(this.mrPath(repo, number), "Get the merge request");
    return this.enrich(repo, data);
  }

  async createPullRequest(repo: RepoRef, input: CreatePrInput): Promise<PrData> {
    const title = input.draft && !/^(draft:|\[draft\])/i.test(input.title) ? `Draft: ${input.title}` : input.title;
    const { data } = await this.json<any>(this.mrPath(repo), "Create the merge request", {
      method: "POST",
      body: { source_branch: input.headBranch, target_branch: input.baseBranch, title, description: input.body, remove_source_branch: false },
    });
    return this.enrich(repo, data);
  }

  private mergeBody(method: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
    const m = (method || "merge").toLowerCase();
    if (m === "rebase") throw new ApiError("invalid_request", "GitLab merges with merge or squash (rebase is a project setting)");
    if (m !== "merge" && m !== "squash") throw new ApiError("invalid_request", "method must be merge, squash or rebase");
    return { squash: m === "squash", ...extra };
  }

  async mergePullRequest(repo: RepoRef, number: number, method: string): Promise<PrData> {
    await this.json<any>(`${this.mrPath(repo, number)}/merge`, "Merge the merge request", { method: "PUT", body: this.mergeBody(method) });
    return this.getPullRequest(repo, number);
  }

  async setAutoMerge(repo: RepoRef, number: number, enabled: boolean, method: string): Promise<void> {
    if (enabled) {
      await this.json<any>(`${this.mrPath(repo, number)}/merge`, "Enable merge when the pipeline succeeds", {
        method: "PUT",
        body: this.mergeBody(method, { merge_when_pipeline_succeeds: true }),
      });
    } else {
      await this.json<any>(`${this.mrPath(repo, number)}/cancel_merge_when_pipeline_succeeds`, "Cancel merge when the pipeline succeeds", { method: "POST" });
    }
  }

  private note(n: any, discussionId: string, mrUrl: string, resolved: boolean): CommentData {
    const pos = n.position ?? {};
    return {
      id: `${discussionId}/${n.id}`,
      author: str(n.author?.username),
      body: str(n.body),
      path: str(pos.new_path ?? pos.old_path),
      line: num(pos.new_line ?? pos.old_line),
      resolved,
      url: mrUrl ? `${mrUrl}#note_${n.id}` : "",
      createdAt: isoTime(n.created_at),
    };
  }

  async listReviewComments(repo: RepoRef, number: number, unresolvedOnly: boolean): Promise<CommentData[]> {
    const [{ data: mr }, { data }] = await Promise.all([
      this.json<any>(this.mrPath(repo, number), "Get the merge request"),
      this.json<any[]>(`${this.mrPath(repo, number)}/discussions?per_page=100`, "List discussions"),
    ]);
    const out: CommentData[] = [];
    for (const d of data ?? []) {
      const notes = (d.notes ?? []).filter((n: any) => !n.system);
      const resolved = notes.some((n: any) => n.resolvable) && notes.filter((n: any) => n.resolvable).every((n: any) => n.resolved);
      for (const n of notes) out.push(this.note(n, str(d.id), str(mr?.web_url), resolved));
    }
    return unresolvedOnly ? out.filter((c) => !c.resolved) : out;
  }

  async postComment(repo: RepoRef, number: number, body: string, inReplyTo: string): Promise<CommentData> {
    if (!body.trim()) throw new ApiError("invalid_request", "body is required");
    const { data: mr } = await this.json<any>(this.mrPath(repo, number), "Get the merge request");
    if (!inReplyTo) {
      const { data } = await this.json<any>(`${this.mrPath(repo, number)}/discussions`, "Post the comment", { method: "POST", body: { body } });
      const n = data?.notes?.[0] ?? {};
      return this.note(n, str(data?.id), str(mr?.web_url), false);
    }
    const discussionId = inReplyTo.split("/")[0]!;
    if (!/^[0-9a-f]{8,64}$/i.test(discussionId)) throw new ApiError("invalid_request", `Invalid comment id ${inReplyTo}`);
    const { data } = await this.json<any>(`${this.mrPath(repo, number)}/discussions/${discussionId}/notes`, "Reply in the discussion", {
      method: "POST",
      body: { body },
    });
    return this.note(data, discussionId, str(mr?.web_url), false);
  }

  async listChecks(repo: RepoRef, sel: { number?: number; sha?: string }) {
    let pipelineId = 0;
    let sha = sel.sha ?? "";
    if (sha) {
      const { data } = await this.json<any[]>(`${this.api}/projects/${project(repo)}/pipelines?sha=${encodeURIComponent(sha)}&per_page=1`, "List pipelines");
      pipelineId = num(data?.[0]?.id);
    } else {
      if (!sel.number) throw new ApiError("invalid_request", "number or sha is required");
      const { data } = await this.json<any>(this.mrPath(repo, sel.number), "Get the merge request");
      sha = str(data?.sha);
      pipelineId = num(data?.head_pipeline?.id ?? data?.pipeline?.id);
      if (!pipelineId) {
        const { data: pipes } = await this.json<any[]>(`${this.mrPath(repo, sel.number)}/pipelines?per_page=1`, "List merge request pipelines");
        pipelineId = num(pipes?.[0]?.id);
      }
    }
    const checks = pipelineId ? await this.pipelineJobs(repo, pipelineId) : [];
    return { checks, summary: summarize(checks), sha };
  }

  async getCheckLog(repo: RepoRef, checkId: string, maxBytes: number) {
    const m = /^(job:)?(\d+)$/.exec(checkId);
    if (!m) throw new ApiError("invalid_request", `Invalid check id ${checkId}`);
    const res = await httpRequest(this.conn, `${this.api}/projects/${project(repo)}/jobs/${m[2]}/trace`, {
      authorization: `Bearer ${this.auth.token}`,
      timeoutMs: 60_000,
    });
    if (!res.ok) throw await upstreamError(res, "Get the job trace", this.auth.userToken);
    return readTail(res, logLimit(maxBytes));
  }
}

/** Repository key of a webhook payload's project object. */
export function gitlabRepoKey(projectObj: any, baseUrl: string): string {
  if (projectObj?.web_url) return repoKeyFromUrl(projectObj.web_url);
  return repoKeyFromUrl(`${baseUrl}/${str(projectObj?.path_with_namespace)}`);
}
