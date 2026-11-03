/**
 * Search providers: the keyed API adapters against a fake HTTP server (request shape, result mapping, error
 * mapping, key scrubbing, timeouts), and the "search" provider group through ProviderService and
 * SearchService (presets, options, the server default and per-user choices, authz, write-only keys).
 */
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { ProviderScope } from "../src/gen/harness/llm/v1/providers_pb.ts";
import { SearchService } from "../src/gen/harness/search/v1/search_pb.ts";
import { createSearchProvider } from "../src/search/adapters.ts";
import { SearxngProvider } from "../src/search/searxng.ts";
import { resetSearchRegistry, SearchRegistry } from "../src/search/service.ts";
import type { SearchType } from "../src/services/presets.ts";
import { createUserAndLogin, deviceLogin, startTestServer, type TestServer } from "./helpers/server.ts";

// Fake keys, never real.
const KEY = "sk-search-fake-key-0000000000";
const USER_KEY = "sk-search-user-key-111111111";

interface Seen {
  method: string;
  path: string;
  params: URLSearchParams;
  headers: IncomingMessage["headers"];
  body: any;
}

let server: Server;
let base = "";
let seen: Seen[] = [];
let reply: { status: number; body: unknown; headers?: Record<string, string>; delayMs?: number } = { status: 200, body: {} };

beforeAll(async () => {
  server = createServer((req, res) => {
    let raw = "";
    req.on("data", (c) => (raw += c));
    req.on("end", () => {
      const u = new URL(req.url ?? "/", "http://x");
      seen.push({ method: req.method ?? "", path: u.pathname, params: u.searchParams, headers: req.headers, body: raw ? JSON.parse(raw) : undefined });
      const send = () => res.writeHead(reply.status, { "content-type": "application/json", ...reply.headers }).end(JSON.stringify(reply.body));
      if (reply.delayMs) setTimeout(send, reply.delayMs);
      else send();
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(() => new Promise<void>((r) => server.close(() => r())));

beforeEach(() => {
  seen = [];
  reply = { status: 200, body: {} };
});

function adapter(type: SearchType, options: Record<string, string> = {}, timeoutMs?: number) {
  return createSearchProvider({ id: type, displayName: type, type, baseUrl: `${base}/search`, apiKey: KEY, options, allowPrivateNetwork: true, timeoutMs });
}

const q = (over: Partial<{ allowedDomains: string[]; blockedDomains: string[]; maxResults: number }> = {}) => ({
  query: "rust async",
  allowedDomains: [],
  blockedDomains: [],
  maxResults: 5,
  ...over,
});

describe("search adapters", () => {
  it("brave: GET with the subscription token, options as params, HTML stripped", async () => {
    reply.body = { web: { results: [{ title: "Async Rust", url: "https://rust-lang.org/async", description: "The <strong>async</strong> book" }] } };
    const hits = await adapter("brave", { safe_search: "strict", country: "DE", language: "de" }).search(q({ allowedDomains: ["rust-lang.org"] }));
    expect(hits).toEqual([{ title: "Async Rust", url: "https://rust-lang.org/async", snippet: "The async book" }]);
    const s = seen[0]!;
    expect(s.method).toBe("GET");
    expect(s.headers["x-subscription-token"]).toBe(KEY);
    expect(s.params.get("q")).toBe("rust async site:rust-lang.org");
    expect(Object.fromEntries(s.params)).toMatchObject({ count: "5", safesearch: "strict", country: "DE", search_lang: "de" });
  });

  it("tavily: POST with a bearer key, domain filters and favicons", async () => {
    reply.body = { results: [{ title: "T", url: "https://a.dev/x", content: "excerpt", published_date: "2026-10-01", favicon: "https://a.dev/f.ico" }] };
    const hits = await adapter("tavily", { safe_search: "strict" }).search(q({ blockedDomains: ["spam.com"] }));
    expect(hits[0]).toEqual({ title: "T", url: "https://a.dev/x", snippet: "excerpt", publishedAt: "2026-10-01T00:00:00.000Z", favicon: "https://a.dev/f.ico" });
    expect(seen[0]!.headers.authorization).toBe(`Bearer ${KEY}`);
    expect(seen[0]!.body).toEqual({ query: "rust async", max_results: 5, include_favicon: true, safe_search: true, exclude_domains: ["spam.com"] });
  });

  it("exa: x-api-key, highlights as the snippet, country as user location", async () => {
    reply.body = { results: [{ title: "E", url: "https://e.dev/", highlights: ["one", "two"], publishedDate: "2026-09-01T10:00:00.000Z", favicon: "https://e.dev/i.png" }] };
    const hits = await adapter("exa", { country: "US" }).search(q({ allowedDomains: ["e.dev"] }));
    expect(hits[0]).toMatchObject({ snippet: "one … two", publishedAt: "2026-09-01T10:00:00.000Z", favicon: "https://e.dev/i.png" });
    expect(seen[0]!.headers["x-api-key"]).toBe(KEY);
    expect(seen[0]!.body).toMatchObject({ query: "rust async", numResults: 5, userLocation: "US", includeDomains: ["e.dev"], contents: { highlights: { maxCharacters: 400 } } });
  });

  it("kagi: bearer key, filters and lens, data.search results", async () => {
    reply.body = { meta: {}, data: { search: [{ title: "K", url: "https://k.dev/", snippet: "s", time: "2026-08-01T00:00:00Z" }] } };
    const hits = await adapter("kagi", { safe_search: "strict", country: "GB" }).search(q({ allowedDomains: ["k.dev"] }));
    expect(hits[0]).toMatchObject({ title: "K", snippet: "s", publishedAt: "2026-08-01T00:00:00.000Z" });
    expect(seen[0]!.headers.authorization).toBe(`Bearer ${KEY}`);
    expect(seen[0]!.body).toEqual({ query: "rust async", limit: 5, filters: { safe_search: true, region: "GB" }, lens: { sites_included: ["k.dev"] } });
  });

  it("perplexity: bearer key, language and domain filters", async () => {
    reply.body = { id: "x", results: [{ title: "P", url: "https://p.dev/", snippet: "snip", date: null }, { title: "bad", url: "javascript:alert(1)", snippet: "" }] };
    const hits = await adapter("perplexity", { country: "FR", language: "fr" }).search(q({ allowedDomains: ["p.dev"] }));
    expect(hits).toEqual([{ title: "P", url: "https://p.dev/", snippet: "snip" }]);
    expect(seen[0]!.body).toEqual({ query: "rust async", max_results: 5, country: "FR", search_language_filter: ["fr"], search_domain_filter: ["p.dev"] });
  });

  it("maps errors and never echoes the key", async () => {
    reply = { status: 401, body: { error: `invalid key ${KEY}` } };
    const err = await adapter("tavily")
      .search(q())
      .catch((e) => e);
    expect(err).toMatchObject({ type: "upstream", upstreamStatus: 401 });
    expect(err.message).toContain("rejected the API key");
    expect(err.message).not.toContain(KEY);
    expect(err.message).toContain("[redacted]");

    reply = { status: 429, body: {}, headers: { "retry-after": "7" } };
    await expect(adapter("brave").search(q())).rejects.toMatchObject({ type: "rate_limit", retryAfterMs: 7000 });
    reply = { status: 402, body: { error: "out of credits" } };
    await expect(adapter("exa").search(q())).rejects.toMatchObject({ type: "quota_exceeded" });
    reply = { status: 432, body: { detail: { error: "plan limit" } } };
    await expect(adapter("tavily").search(q())).rejects.toMatchObject({ type: "quota_exceeded", message: expect.stringContaining("plan limit") });
    reply = { status: 500, body: "<html>oops</html>" };
    await expect(adapter("kagi").search(q())).rejects.toMatchObject({ type: "upstream", message: "kagi returned HTTP 500" });
  });

  it("times out", async () => {
    reply = { status: 200, body: {}, delayMs: 300 };
    await expect(adapter("perplexity", {}, 50).search(q())).rejects.toMatchObject({ type: "upstream", message: expect.stringContaining("in time") });
  });

  it("needs a key", async () => {
    const p = createSearchProvider({ id: "b", displayName: "Brave", type: "brave", baseUrl: `${base}/search`, options: {}, allowPrivateNetwork: true });
    await expect(p.search(q())).rejects.toMatchObject({ type: "failed_precondition" });
    expect(await p.healthy()).toBe(false);
  });
});

// --- Through the services ------------------------------------------------------------------------

let ts: TestServer;
let builtin: Server;
let builtinUrl = "";
let admin: ReturnType<TestServer["as"]>;
let alice: ReturnType<TestServer["as"]>;
let bob: ReturnType<TestServer["as"]>;
let aliceSearch: Client<typeof SearchService>;
let bobSearch: Client<typeof SearchService>;

function searchClient(token: string) {
  const transport = createConnectTransport({
    baseUrl: ts.url,
    httpVersion: "1.1",
    interceptors: [
      (next) => async (req) => {
        req.header.set("authorization", `Bearer ${token}`);
        return next(req);
      },
    ],
  });
  return createClient(SearchService, transport);
}

async function code(p: Promise<unknown>): Promise<Code | undefined> {
  try {
    await p;
    return undefined;
  } catch (e) {
    return (e as ConnectError).code;
  }
}

describe("search providers group", () => {
  beforeAll(async () => {
    // The built-in SearXNG: a second fake answering with its own result.
    builtin = createServer((_req, res) => res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ results: [{ title: "builtin", url: "https://builtin.example/" }] })));
    await new Promise<void>((r) => builtin.listen(0, "127.0.0.1", r));
    builtinUrl = `http://127.0.0.1:${(builtin.address() as AddressInfo).port}`;
    resetSearchRegistry(new SearchRegistry([new SearxngProvider({ baseUrl: builtinUrl })], "searxng"));

    ts = await startTestServer();
    const a = await deviceLogin(ts);
    admin = ts.as(a.accessToken);
    const al = await createUserAndLogin(ts, a.cookie, "alice-search@test.local");
    const bo = await createUserAndLogin(ts, a.cookie, "bob-search@test.local");
    alice = ts.as(al.accessToken);
    bob = ts.as(bo.accessToken);
    aliceSearch = searchClient(al.accessToken);
    bobSearch = searchClient(bo.accessToken);
  });

  afterAll(async () => {
    await ts.close();
    resetSearchRegistry();
    await new Promise<void>((r) => builtin.close(() => r()));
  });

  it("lists search presets with their options", async () => {
    const r = await alice.providers.listPresets({});
    const search = r.presets.filter((p) => p.group === "search");
    expect(search.map((p) => p.id)).toEqual(["searxng", "searxng-url", "brave", "tavily", "exa", "kagi", "perplexity"]);
    expect(search[0]).toMatchObject({ builtin: true, fields: [] });
    const brave = search.find((p) => p.id === "brave")!;
    expect(brave).toMatchObject({ type: "brave", baseUrl: "https://api.search.brave.com/res/v1/web/search" });
    expect(brave.options.map((o) => o.name)).toEqual(["max_results", "safe_search", "country", "language"]);
    expect(brave.options[0]).toMatchObject({ kind: "number", min: 1, max: 20 });
    expect(r.protocols.some((p) => p.group === "search" && p.id === "searxng")).toBe(true);
  });

  it("uses the built-in SearXNG until someone chooses otherwise", async () => {
    const s = await alice.providers.getSearch({});
    expect(s.settings).toMatchObject({ defaultProviderId: "searxng", myProviderId: "", effectiveProviderId: "searxng", editable: false });
    const out = await aliceSearch.search({ query: "hello" });
    expect(out.provider).toBe("searxng");
    expect(out.results[0]!.title).toBe("builtin");
    expect((await alice.providers.testProvider({ providerId: "searxng" })).ok).toBe(true);
    expect(await code(admin.providers.createProvider({ scope: ProviderScope.GLOBAL, presetId: "searxng" }))).toBe(Code.InvalidArgument);
    expect(await code(admin.providers.createProvider({ scope: ProviderScope.GLOBAL, group: "search", type: "searxng", id: "searxng", baseUrl: base, allowPrivateNetwork: true }))).toBe(
      Code.InvalidArgument,
    );
  });

  it("admin default for everyone, per-user override, stale choices fall back", async () => {
    // A global SearXNG on the fake server (private network allowed for global providers).
    const created = await admin.providers.createProvider({
      scope: ProviderScope.GLOBAL,
      presetId: "searxng-url",
      baseUrl: base,
      options: { max_results: "3", safe_search: "strict", language: "en" },
    });
    const g = created.provider!;
    expect(g).toMatchObject({ group: "search", presetId: "searxng-url", options: { max_results: "3", safe_search: "strict", language: "en" } });

    // Users cannot set the default; admins can.
    expect(await code(alice.providers.setSearchDefault({ providerId: g.id }))).toBe(Code.PermissionDenied);
    const d = await admin.providers.setSearchDefault({ providerId: g.id });
    expect(d.settings).toMatchObject({ defaultProviderId: g.id, effectiveProviderId: g.id, editable: true });

    reply.body = { results: Array.from({ length: 8 }, (_, i) => ({ title: `r${i}`, url: `https://r.dev/${i}` })) };
    const out = await aliceSearch.search({ query: "hello" });
    expect(out.provider).toBe(g.id);
    // The provider's result count applies when the request leaves it at 0.
    expect(out.results).toHaveLength(3);
    expect(seen.at(-1)!.params.get("safesearch")).toBe("2");
    expect(seen.at(-1)!.params.get("language")).toBe("en");

    // Bob prefers the built-in for himself; Alice still follows the default.
    const b = await bob.providers.setMySearch({ providerId: "searxng" });
    expect(b.settings).toMatchObject({ myProviderId: "searxng", effectiveProviderId: "searxng", defaultProviderId: g.id });
    expect((await bobSearch.search({ query: "x" })).provider).toBe("searxng");
    expect((await aliceSearch.search({ query: "x" })).provider).toBe(g.id);
    const list = await bobSearch.listProviders({});
    expect(list.defaultProvider).toBe("searxng");
    expect(list.providers.map((p) => p.id)).toEqual(["searxng", g.id]);

    // Disabling the global provider makes the default stale: everyone is back on the built-in.
    await admin.providers.updateProvider({ id: g.id, enabled: false });
    expect((await alice.providers.getSearch({})).settings?.effectiveProviderId).toBe("searxng");
    expect(await code(aliceSearch.search({ query: "x", provider: g.id }))).toBe(Code.InvalidArgument);
    await admin.providers.updateProvider({ id: g.id, enabled: true });
    await bob.providers.setMySearch({ providerId: "" });
    expect((await bob.providers.getSearch({})).settings?.effectiveProviderId).toBe(g.id);
  });

  it("personal search providers: own key, write-only, invisible to others", async () => {
    const p = (await alice.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "brave", apiKey: USER_KEY, options: { country: "de" } })).provider!;
    expect(p).toMatchObject({ scope: ProviderScope.PERSONAL, group: "search", apiKeySet: true, options: { country: "DE" } });
    expect(JSON.stringify(p)).not.toContain(USER_KEY);
    expect(JSON.stringify(await alice.providers.listProviders({}))).not.toContain(USER_KEY);
    expect(JSON.stringify(await admin.providers.listProviders({}))).not.toContain(USER_KEY);

    const mine = await alice.providers.setMySearch({ providerId: p.id });
    expect(mine.settings).toMatchObject({ myProviderId: p.id, effectiveProviderId: p.id });
    // Bob cannot pick, search with, test or see Alice's provider; nor can an admin make it the default.
    expect(await code(bob.providers.setMySearch({ providerId: p.id }))).toBe(Code.InvalidArgument);
    expect(await code(bobSearch.search({ query: "x", provider: p.id }))).toBe(Code.InvalidArgument);
    expect(await code(bob.providers.testProvider({ providerId: p.id }))).toBe(Code.NotFound);
    expect(await code(admin.providers.setSearchDefault({ providerId: p.id }))).toBe(Code.InvalidArgument);
    expect((await bob.providers.listProviders({})).providers.some((x) => x.id === p.id)).toBe(false);

    // Options are validated against the preset; models are not a search provider's thing.
    expect(await code(alice.providers.updateProvider({ id: p.id, options: { values: { max_results: "50" } } }))).toBe(Code.InvalidArgument);
    expect(await code(alice.providers.updateProvider({ id: p.id, options: { values: { colour: "red" } } }))).toBe(Code.InvalidArgument);
    const u = await alice.providers.updateProvider({ id: p.id, options: { values: { max_results: "7", safe_search: "off" } } });
    expect(u.provider?.options).toEqual({ max_results: "7", safe_search: "off" });
    expect(await code(alice.providers.addModel({ providerId: p.id, upstreamModel: "x" }))).toBe(Code.InvalidArgument);
    expect(await code(alice.providers.discoverModels({ providerId: p.id }))).toBe(Code.InvalidArgument);

    // Personal providers cannot reach private networks.
    expect(await code(bob.providers.createProvider({ scope: ProviderScope.PERSONAL, presetId: "searxng-url", baseUrl: base }))).toBe(Code.PermissionDenied);

    // Deleting it puts Alice back on the default.
    await alice.providers.deleteProvider({ id: p.id });
    expect((await alice.providers.getSearch({})).settings?.myProviderId).toBe("");
  });

  it("tests a connected provider with a sample query", async () => {
    const created = await admin.providers.createProvider({ scope: ProviderScope.GLOBAL, group: "search", type: "searxng", displayName: "Lab SearXNG", baseUrl: base, allowPrivateNetwork: true });
    reply.body = { results: [{ title: "a", url: "https://a.dev/" }] };
    const ok = await admin.providers.testProvider({ providerId: created.provider!.id });
    expect(ok).toMatchObject({ ok: true, message: expect.stringContaining("1 result") });
    reply = { status: 403, body: {} };
    const bad = await admin.providers.testProvider({ providerId: created.provider!.id });
    expect(bad.ok).toBe(false);
    expect(bad.message).toContain("JSON format");
    // Custom search providers speak SearXNG only.
    expect(await code(admin.providers.createProvider({ scope: ProviderScope.GLOBAL, group: "search", type: "brave", baseUrl: "https://1.1.1.1" }))).toBe(Code.InvalidArgument);
  });
});
