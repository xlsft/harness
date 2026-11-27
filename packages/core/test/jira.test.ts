/**
 * Jira and Confluence on a fake Atlassian site: each sign-in, search and paging, a missing issue, field updates and
 * transitions, Confluence with its own sign-in and borrowing Jira's, and the registry lines. No real account.
 */
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { CONFLUENCE, confluenceSite } from "../src/knowledge/confluence.ts";
import { ATLASSIAN_OAUTH, atlassian, cql, JIRA, jiraTime, openJira } from "../src/tracker/jira.ts";
import type { OpenOptions } from "../src/tracker/provider.ts";
import { knowledgeModule, trackerModule } from "../src/tracker/registry.ts";
import { FakeJira } from "./helpers/fake-trackers.ts";

describe("Jira", () => {
  const jira = new FakeJira();
  const isCloud = atlassian.isCloud;
  const opts = (o: Partial<OpenOptions>): OpenOptions => ({ baseUrl: jira.url, auth: "email_token", login: jira.email, oauthClientId: "", credentials: { token: jira.apiToken }, options: {}, allowPrivateNetwork: true, retryDelaysMs: [], ...o });

  beforeAll(async () => {
    await jira.start();
    atlassian.isCloud = (u) => u.origin === jira.url;
  });
  afterAll(async () => {
    atlassian.isCloud = isCloud;
    await jira.close();
  });

  it("is registered on both lists", () => {
    expect(trackerModule("jira")).toBe(JIRA);
    expect(knowledgeModule("confluence")).toBe(CONFLUENCE);
    expect(CONFLUENCE.capabilities.borrowFrom).toEqual(["jira"]);
  });

  it("signs in to Cloud with an email and API token, searches and reads", async () => {
    jira.issues = [
      { key: "DEMO-1", summary: "One", status: "To Do", labels: ["a"] },
      { key: "DEMO-2", summary: "Two", status: "Done", labels: [] },
    ];
    const j = JIRA.open(opts({}));
    expect(await j.test()).toEqual({ login: jira.email, displayName: "Alice" });
    const r = await j.search('project = DEMO AND status = "To Do"', { limit: 10 });
    expect(r.issues.map((i) => i.id)).toEqual(["DEMO-1"]);
    expect(r.hasMore).toBe(false);
    const one = await j.get("DEMO-1");
    expect(one.description).toBe("Body of DEMO-1");
    expect(one.comments[0]!.author).toBe("Bob");
    await expect(j.get("DEMO-404")).rejects.toMatchObject({ kind: "not_found" });
    await j.update("DEMO-1", { fields: [{ name: "Status", value: "in review" }] });
    expect(jira.issues[0]!.status).toBe("In Review");
    await j.comment("DEMO-1", "looked");
    expect(jira.comments.at(-1)).toEqual({ key: "DEMO-1", body: "looked" });
  });

  it("refuses a wrong token as an auth failure", async () => {
    await expect(JIRA.open(opts({ credentials: { token: "wrong" } })).test()).rejects.toMatchObject({ kind: "auth" });
  });

  it("runs on Data Center with a personal access token", async () => {
    atlassian.isCloud = () => false;
    try {
      jira.issues = [{ key: "DEMO-5", summary: "Five", status: "To Do", labels: [] }];
      const j = openJira(opts({ auth: "token", login: "", credentials: { token: jira.pat } }));
      const r = await j.search("project = DEMO", { limit: 10 });
      expect(r.issues.map((i) => i.id)).toEqual(["DEMO-5"]);
      await j.update("DEMO-5", { fields: [{ name: "Points", value: "3" }] });
      expect(jira.fields["DEMO-5"]).toEqual({ customfield_1: 3 });
    } finally {
      atlassian.isCloud = (u) => u.origin === jira.url;
    }
  });

  it("starts OAuth at Atlassian on Cloud and exchanges the code with PKCE", async () => {
    const old = { ...atlassian };
    atlassian.authUrl = jira.url;
    try {
      const u = new URL(await ATLASSIAN_OAUTH.authorizeUrl({ baseUrl: jira.url, clientId: "cli", redirectUri: "http://127.0.0.1:1/cb", state: "st", challenge: "ch", allowPrivateNetwork: true, options: {} }));
      expect(u.searchParams.get("audience")).toBe("api.atlassian.com");
      expect(u.searchParams.get("code_challenge")).toBe("ch");
      const t = await ATLASSIAN_OAUTH.exchangeCode({ baseUrl: jira.url, clientId: "cli", code: "atl-code", redirectUri: "http://127.0.0.1:1/cb", verifier: "v", allowPrivateNetwork: true, options: {} });
      expect(t.accessToken).toBe("atl-access-1");
      expect(jira.tokenRequests.at(-1)!.code_verifier).toBe("v");
    } finally {
      atlassian.authUrl = old.authUrl;
    }
  });

  it("reads Confluence with its own sign-in and with Jira's", async () => {
    const own = CONFLUENCE.open({ ...opts({ baseUrl: `${jira.url}/wiki` }), borrowedFrom: "", trackerBaseUrl: "" });
    const hits = await own.searchArticles("deploy", "space = HELP", 5);
    expect(hits[0]).toMatchObject({ id: "1", title: "Deploy guide", url: `${jira.url}/wiki/spaces/HELP/pages/1` });
    const borrowed = CONFLUENCE.open({ ...opts({}), borrowedFrom: "jira", trackerBaseUrl: jira.url });
    expect((await borrowed.getArticle("1")).content).toContain("deploy with care & patience.");
    // On Data Center a borrowed sign-in needs Confluence's own address.
    expect(confluenceSite({ baseUrl: "https://jira.corp.example", borrowedFrom: "jira", trackerBaseUrl: "https://jira.corp.example" })).toEqual({ site: "https://jira.corp.example", wiki: "" });
    expect(confluenceSite({ baseUrl: "https://wiki.corp.example", borrowedFrom: "jira", trackerBaseUrl: "https://jira.corp.example" })).toEqual({ site: "https://jira.corp.example", wiki: "https://wiki.corp.example" });
  });

  it("builds CQL and reads Jira's times", () => {
    expect(cql('deploy "now"', "space = HELP")).toBe('type = page AND (space = HELP) AND text ~ "deploy \\"now\\""');
    expect(jiraTime("2026-01-02T03:04:05.000+0000")).toBe("2026-01-02T03:04:05Z");
  });
});
