/**
 * Internal, JSON-friendly shapes of the VcsService data (proto/harness/vcs/v1/vcs.proto). Provider clients
 * return these; they are stored as JSON (vcs_pull_request, vcs_event) and converted to protobuf in
 * src/rpc/vcs.ts. Field names follow the protobuf-es local names, so conversion is a copy plus int64s.
 */
import type { PullRequest_State } from "../gen/harness/agent/v1/agent_pb.ts";
import type { CheckState } from "../gen/harness/vcs/v1/vcs_pb.ts";

/** A parsed repository key: "host/owner/repo" (GitLab: "host/group/sub/repo"), lowercased. */
export interface RepoRef {
  key: string;
  host: string;
  /** owner/repo (GitLab: full namespace path), lowercased. */
  path: string;
  /** First path segment (GitHub: the account that owns the repo). */
  owner: string;
  /** Last path segment. */
  name: string;
}

export interface RepoData {
  connectionId: string;
  key: string;
  fullName: string;
  defaultBranch: string;
  url: string;
  private: boolean;
}

export interface ChecksSummaryData {
  state: CheckState;
  total: number;
  passed: number;
  failed: number;
  running: number;
}

export interface CheckData {
  /** "cr:<id>" (GitHub check run), "st:<id>" (GitHub commit status), "job:<id>" (GitLab job). */
  id: string;
  name: string;
  state: CheckState;
  url: string;
  workflow: string;
  durationMs: number;
  summary: string;
}

/** "approved" | "changes_requested" | "review_required" | "none" */
export type ReviewState = string;

export interface PrData {
  repositoryKey: string;
  number: number;
  title: string;
  url: string;
  headBranch: string;
  baseBranch: string;
  author: string;
  state: PullRequest_State;
  reviewState: ReviewState;
  checks?: ChecksSummaryData;
  mergeable: boolean;
  hasConflicts: boolean;
  autoMergeEnabled: boolean;
  draft: boolean;
  /** RFC 3339, UTC. */
  updatedAt: string;
  // --- internal (not part of the proto message) ---
  /** Head commit SHA. */
  headSha?: string;
  /** GitHub GraphQL node id (auto-merge). */
  nodeId?: string;
  /** Latest review state per reviewer login (from webhooks), to derive reviewState. */
  reviewers?: Record<string, string>;
}

export interface CommentData {
  /** GitHub: "r:<id>" (review comment) or "c:<id>" (conversation); GitLab: "<discussion id>/<note id>". */
  id: string;
  author: string;
  body: string;
  path: string;
  line: number;
  resolved: boolean;
  url: string;
  createdAt: string;
}

export interface CreatePrInput {
  headBranch: string;
  baseBranch: string;
  title: string;
  body: string;
  draft: boolean;
}

/** Operations every server provider implements (src/vcs/github.ts, src/vcs/gitlab.ts). */
export interface VcsProviderClient {
  /** Login of the token's account ("" for app/installation tokens). */
  whoami(): Promise<string>;
  /** Whether the token can see the repository. */
  canAccess(repo: RepoRef): Promise<boolean>;
  /** Whether a user of the code host, by numeric id, can push to the repository (GitLab: comments do not say). */
  canWrite?(repo: RepoRef, userId: number): Promise<boolean>;
  listRepositories(query: string, pageToken: string): Promise<{ repositories: RepoData[]; nextPageToken: string }>;
  listPullRequests(repo: RepoRef, opts: { headBranch: string; includeClosed: boolean }): Promise<PrData[]>;
  getPullRequest(repo: RepoRef, number: number): Promise<PrData>;
  createPullRequest(repo: RepoRef, input: CreatePrInput): Promise<PrData>;
  mergePullRequest(repo: RepoRef, number: number, method: string): Promise<PrData>;
  setAutoMerge(repo: RepoRef, number: number, enabled: boolean, method: string): Promise<void>;
  listReviewComments(repo: RepoRef, number: number, unresolvedOnly: boolean): Promise<CommentData[]>;
  postComment(repo: RepoRef, number: number, body: string, inReplyTo: string): Promise<CommentData>;
  listChecks(repo: RepoRef, sel: { number?: number; sha?: string }): Promise<{ checks: CheckData[]; summary: ChecksSummaryData; sha: string }>;
  getCheckLog(repo: RepoRef, checkId: string, maxBytes: number): Promise<{ log: string; truncated: boolean }>;
}
