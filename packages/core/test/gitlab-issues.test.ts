/**
 * GitLab Issues on a fake GitLab (/api/v4), signed in through a code host connection: filters map to the list API, nested
 * group paths, notes without system ones, changes in one PUT, and a GitHub connection or a bad token read as errors.
 */
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { GITLAB, parseGitLabId, parseGitLabQuery } from "../src/tracker/gitlab.ts";
import type { OpenOptions, VcsSignIn } from "../src/tracker/provider.ts";
import { trackerModule } from "../src/tracker/registry.ts";

const issue = (iid: number) => ({
  iid,
  title: "Issue",
  description: "Body",
  web_url: `https://gl.test/grp/sub/app/-/issues/${iid}`,
  state: "opened",
  updated_at: "2026-01-02T03:04:05Z",
  labels: ["bug"],
  assignees: [{ id: 1, username: "alice" }],
  references: { full: `grp/sub/app#${iid}` },
});

describe("GitLab Issues", () => {
  const seen = { lists: [] as string[], puts: [] as Record<string, unknown>[], notes: [] as string[] };
  let server: Server;
  let url = "";
  const proj = "projects/grp%2Fsub%2Fapp";

  beforeAll(async () => {
    server = createServer(async (req, res) => {
      const send = (status: number, body?: unknown) => {
        res.writeHead(status, { "content-type": "application/json" });
        res.end(body === undefined ? "" : JSON.stringify(body));
      };
      if (req.headers.authorization !== "Bearer gl-token") return send(401, { message: "401 Unauthorized" });
      let raw = "";
      for await (const c of req) raw += c;
      const body = raw ? JSON.parse(raw) : {};
      const [pathname, search = ""] = (req.url ?? "/").split("?");
      const p = pathname!.replace(/^\/api\/v4\//, "");
      const m = req.method;
      if (p === "user") return send(200, { id: 1, username: "alice", name: "Alice" });
      if (p === "users") return send(200, [{ id: 2, username: new URLSearchParams(search).get("username") }]);
      if ((p === "issues" || p === `${proj}/issues`) && m === "GET") {
        seen.lists.push(`${p}?${search}`);
        return send(200, [issue(1), issue(3)]);
      }
      if (p === `${proj}/issues/1` && m === "GET") return send(200, issue(1));
      if (p === `${proj}/issues/1` && m === "PUT") {
        seen.puts.push(body);
        return send(200, issue(1));
      }
      if (p === `${proj}/issues/1/notes` && m === "GET")
        return send(200, [
          { body: "newest", created_at: "2026-01-03T00:00:00Z", author: { username: "bob" } },
          { body: "changed the label", system: true, created_at: "2026-01-02T12:00:00Z", author: { username: "bob" } },
          { body: "first", created_at: "2026-01-02T00:00:00Z", author: { username: "carol" } },
        ]);
      if (p === `${proj}/issues/1/notes`) {
        seen.notes.push(body.body);
        return send(201, { id: 1 });
      }
      if (p === `${proj}/issues` && m === "POST") return send(201, issue(7));
      if (p === `${proj}/milestones`) return send(200, [{ id: 40, title: "v1" }]);
      if (p === `${proj}/labels`) return send(200, [{ name: "bug" }, { name: "review" }]);
      if (p === `${proj}/members/all`) return send(200, [{ id: 1, username: "alice" }]);
      if (p === "projects") return send(200, [{ path_with_namespace: "grp/sub/app", name: "app" }]);
      return send(404, { message: "404 Not Found" });
    });
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });
  afterAll(() => new Promise<void>((r) => server.close(() => r())));

  const signIn = (o: Partial<VcsSignIn> = {}) => async (): Promise<VcsSignIn> => ({ type: "gitlab", baseUrl: url, token: async () => "gl-token", ...o });
  const opts = (vcs = signIn()): OpenOptions => ({ baseUrl: "https://gitlab.com", auth: "vcs", login: "", oauthClientId: "", credentials: {}, options: {}, allowPrivateNetwork: true, retryDelaysMs: [], vcs });

  it("is registered and reads ids and filters", () => {
    expect(trackerModule("gitlab")).toBe(GITLAB);
    expect(parseGitLabId("grp/sub/app#12")).toEqual({ project: "grp/sub/app", iid: 12 });
    expect(() => parseGitLabId("app#1")).toThrow();
    expect(parseGitLabQuery("group:grp author:bob")).toEqual({ path: "groups/grp/issues", params: { author_username: "bob" } });
    expect(parseGitLabQuery("crash")).toEqual({ path: "issues", params: { search: "crash", scope: "all" } });
    expect(() => parseGitLabQuery("colour:red")).toThrow(/Unknown filter/);
  });

  it("searches, reads, changes, comments and creates", async () => {
    const gl = GITLAB.open(opts());
    expect((await gl.test()).login).toBe("alice");
    const found = await gl.search('project:grp/sub/app state:open label:bug assignee:me "slow login"', { limit: 1 });
    expect(found).toMatchObject({ hasMore: true, issues: [{ id: "grp/sub/app#1" }] });
    const q = seen.lists[0]!;
    expect(q.startsWith(`${proj}/issues?`)).toBe(true);
    expect(q).toContain("assignee_username=alice");
    expect(q).toContain("search=slow+login");
    expect(q).toContain("state=opened");

    const one = await gl.get("grp/sub/app#1");
    expect(one.comments.map((c) => c.text)).toEqual(["first", "newest"]);
    await expect(gl.get("grp/sub/app#404")).rejects.toMatchObject({ kind: "not_found" });

    await gl.update("grp/sub/app#1", { fields: [{ name: "State", value: "closed" }, { name: "Add labels", value: "review" }, { name: "Remove labels", value: "bug" }, { name: "Assignees", value: "me, bob" }, { name: "Milestone", value: "v1" }] });
    expect(seen.puts[0]).toEqual({ state_event: "close", add_labels: "review", remove_labels: "bug", assignee_ids: [1, 2], milestone_id: 40 });
    await expect(gl.update("grp/sub/app#1", { fields: [{ name: "State", value: "limbo" }] })).rejects.toMatchObject({ kind: "other" });

    await gl.comment("grp/sub/app#1", "shipped");
    expect(seen.notes).toEqual(["shipped"]);
    expect((await gl.create({ project: "grp/sub/app", title: "New" })).id).toBe("grp/sub/app#7");
    const fields = await gl.fields("grp/sub/app");
    expect(fields[1]?.values).toEqual(["bug", "review"]);
    expect(fields[5]?.values).toEqual(["v1"]);
    expect((await gl.projects()).map((p) => p.key)).toEqual(["grp/sub/app"]);
  });

  it("reads a bad token and a GitHub connection as a tracker's errors", async () => {
    await expect(GITLAB.open(opts(signIn({ token: async () => "wrong" }))).test()).rejects.toMatchObject({ kind: "auth" });
    await expect(GITLAB.open(opts(signIn({ type: "github" }))).test()).rejects.toMatchObject({ kind: "other" });
  });
});
