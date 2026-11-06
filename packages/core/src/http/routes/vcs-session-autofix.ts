/**
 * GET /api/vcs/sessions/:id/autofix-jobs — auto-fix jobs recorded for a session (owner only), newest first.
 * Not part of the RPC contract; the jobs reach the session through the relay (src/vcs/pipeline.ts).
 */
import { listAutoFixJobs } from "../../vcs/pipeline.ts";
import { api, param, requirePrincipal } from "../util.ts";

export default api(async (event) => {
  const p = await requirePrincipal(event);
  const jobs = await listAutoFixJobs(p, param(event, "id"), Number(event.url.searchParams.get("limit") ?? "") || 50);
  return {
    jobs: jobs.map((j) => ({
      id: j.id,
      session_id: j.sessionId,
      event_id: `vev_${j.eventSeq}`,
      kind: j.kind,
      repository_key: j.repositoryKey,
      pr_number: j.prNumber,
      head_sha: j.headSha,
      status: j.status,
      payload: j.payload,
      created_at: j.createdAt.toISOString(),
      updated_at: j.updatedAt.toISOString(),
    })),
  };
});
