/**
 * Builds VcsProviderClients for a connection.
 *
 * Token choice:
 * - user client: the user's own OAuth token (create / merge / comment are attributed to the user);
 * - system client: the GitHub App's installation tokens, or a GitLab connection's group/project access
 *   token. Used for reads on behalf of users without a token (subject to the access rules in service.ts),
 *   webhook follow-ups (conflict re-checks, auto-fix log excerpts) and health checks.
 */
import { ProviderType } from "../gen/harness/vcs/v1/vcs_pb.ts";
import { getUserToken } from "./auth.ts";
import { connSecrets, type ConnectionRow } from "./connections.ts";
import { hasAppCredentials } from "./github-app.ts";
import { GitHubClient } from "./github.ts";
import { GitLabClient } from "./gitlab.ts";
import type { VcsProviderClient } from "./types.ts";

export function userClientWithToken(conn: ConnectionRow, token: string): VcsProviderClient {
  return conn.type === ProviderType.GITLAB ? new GitLabClient(conn, { token, userToken: true }) : new GitHubClient(conn, { userToken: token });
}

export async function userClient(conn: ConnectionRow, userId: string): Promise<VcsProviderClient | undefined> {
  const token = await getUserToken(userId, conn);
  return token ? userClientWithToken(conn, token) : undefined;
}

export function systemClient(conn: ConnectionRow): VcsProviderClient | undefined {
  if (conn.type === ProviderType.GITHUB) return hasAppCredentials(conn) ? new GitHubClient(conn, {}) : undefined;
  const token = connSecrets(conn).accessToken;
  return token ? new GitLabClient(conn, { token, userToken: false }) : undefined;
}
