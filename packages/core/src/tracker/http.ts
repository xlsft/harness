/**
 * JSON over HTTP for the Jira, Linear and GitHub providers, as packages/client/tracker/httpjson.go does on the client. Every
 * request goes through the SSRF guard; errors name the path and status, never a credential.
 */
import { ApiError } from "../lib/errors.ts";
import { guardedFetch } from "../lib/ssrf.ts";
import { TrackerError, type TrackerErrorKind } from "./provider.ts";

export function statusKind(status: number): TrackerErrorKind {
  if (status === 404 || status === 410) return "not_found";
  if (status === 401) return "auth";
  if (status === 403) return "forbidden";
  if (status === 429 || status >= 500) return "transient";
  return "other";
}

/** A body that is an HTML page, as a proxy answers, becomes its title; long ones are cut. */
export function bodySummary(body: string): string {
  let b = body.trim();
  if (/^<!doctype html|^<html/i.test(b)) {
    const m = /<title[^>]*>([\s\S]*?)<\/title>/i.exec(b);
    b = `(HTML page) ${(m ? m[1]! : b.replace(/<[^>]*>/g, " ")).replace(/\s+/g, " ").trim()}`;
  }
  return b.length > 300 ? `${b.slice(0, 300)}…` : b;
}

export interface JsonClientOptions {
  base: string;
  name: string;
  allowPrivateNetwork: boolean;
  /** The request's credentials; may refresh them. */
  authorize?: () => Promise<Record<string, string>>;
  headers?: Record<string, string>;
  retryDelaysMs?: number[];
}

export interface HttpErrorDetail {
  status: number;
  body: string;
}

/** The TrackerError of a status answer keeps the body for providers that read it (GraphQL errors). */
export class TrackerHttpError extends TrackerError {
  constructor(
    kind: TrackerErrorKind,
    message: string,
    readonly detail: HttpErrorDetail,
  ) {
    super(kind, message);
  }
}

const DEFAULT_RETRIES = [500, 1000];

export class JsonClient {
  constructor(readonly o: JsonClientOptions) {}

  private async fetch(url: string, init: { method: string; headers: Record<string, string>; body?: string }) {
    try {
      return await guardedFetch(url, { ...init, allowPrivateNetwork: this.o.allowPrivateNetwork, signal: AbortSignal.timeout(30_000) });
    } catch (err) {
      if (err instanceof TrackerError) throw err;
      if (err instanceof ApiError) throw new TrackerError("other", err.message);
      throw new TrackerError("transient", `Can't reach ${this.o.name}: ${(err as Error).message}`);
    }
  }

  async request<T>(method: string, path: string, query?: Record<string, string | number | undefined>, body?: unknown): Promise<{ data: T; headers: Headers }> {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined) q.set(k, String(v));
    const url = /^https?:\/\//.test(path) ? path : `${this.o.base.replace(/\/+$/, "")}/${path.replace(/^\/+/, "")}`;
    const full = q.size ? `${url}?${q}` : url;
    const payload = body === undefined ? undefined : JSON.stringify(body);
    const delays = this.o.retryDelaysMs ?? DEFAULT_RETRIES;
    for (let attempt = 0; ; attempt++) {
      let res;
      try {
        const headers: Record<string, string> = { accept: "application/json", ...this.o.headers, ...(payload ? { "content-type": "application/json" } : {}), ...(await this.o.authorize?.()) };
        res = await this.fetch(full, { method, headers, body: payload });
      } catch (err) {
        if (err instanceof TrackerError && err.kind === "transient" && method === "GET" && attempt < delays.length) {
          await new Promise((r) => setTimeout(r, delays[attempt]));
          continue;
        }
        throw err;
      }
      const text = await res.text();
      if (!res.ok) {
        const retry = res.status === 429 || res.status === 503 || (method === "GET" && res.status >= 500);
        if (retry && attempt < delays.length) {
          const after = Number(res.headers.get("retry-after"));
          await new Promise((r) => setTimeout(r, after > 0 ? Math.min(after * 1000, 10_000) : delays[attempt]));
          continue;
        }
        const p = new URL(full).pathname;
        throw new TrackerHttpError(statusKind(res.status), `${method} ${p}: ${res.status}${text.trim() ? `: ${bodySummary(text)}` : ""}`, { status: res.status, body: text.slice(0, 64_000) });
      }
      if (!text.trim()) return { data: undefined as T, headers: res.headers as unknown as Headers };
      const type = res.headers.get("content-type") ?? "";
      if (type && !type.includes("json")) throw new TrackerError("other", `Unexpected ${type} from ${this.o.name} at ${new URL(full).host} (check the address, VPN and proxy): ${bodySummary(text)}`);
      try {
        return { data: JSON.parse(text) as T, headers: res.headers as unknown as Headers };
      } catch {
        throw new TrackerError("other", `${method} ${new URL(full).pathname}: invalid JSON`);
      }
    }
  }

  async get<T>(path: string, query?: Record<string, string | number | undefined>): Promise<T> {
    return (await this.request<T>("GET", path, query)).data;
  }

  async send<T = unknown>(method: string, path: string, body?: unknown, query?: Record<string, string | number | undefined>): Promise<T> {
    return (await this.request<T>(method, path, query, body)).data;
  }
}

export interface OAuthTokens {
  accessToken: string;
  refreshToken?: string;
  /** Unix milliseconds; 0 when unknown. */
  expiresAt: number;
}

/** A token request, form-encoded unless asJson. */
export async function oauthTokens(url: string, form: Record<string, string>, opts: { asJson: boolean; allowPrivateNetwork: boolean; name: string }): Promise<OAuthTokens> {
  let res;
  try {
    res = await guardedFetch(url, {
      method: "POST",
      headers: { accept: "application/json", "content-type": opts.asJson ? "application/json" : "application/x-www-form-urlencoded" },
      body: opts.asJson ? JSON.stringify(form) : new URLSearchParams(form).toString(),
      allowPrivateNetwork: opts.allowPrivateNetwork,
      signal: AbortSignal.timeout(30_000),
    });
  } catch (err) {
    if (err instanceof ApiError) throw new TrackerError("other", err.message);
    throw new TrackerError("transient", `Can't reach ${opts.name}: ${(err as Error).message}`);
  }
  const text = await res.text();
  let j: { access_token?: string; refresh_token?: string; expires_in?: number; error?: string; error_description?: string } = {};
  try {
    j = JSON.parse(text);
  } catch {
    /* the message below says what came back */
  }
  if (!res.ok || !j.access_token) {
    const why = [j.error, j.error_description].filter(Boolean).join(": ") || bodySummary(text);
    const kind = res.status === 400 || res.status === 401 ? "auth" : statusKind(res.status);
    throw new TrackerError(kind, `The token endpoint answered ${res.status}${why ? `: ${why}` : ""}: authorize this connection again`);
  }
  return { accessToken: j.access_token, refreshToken: j.refresh_token, expiresAt: j.expires_in ? Date.now() + j.expires_in * 1000 : 0 };
}

/** Keeps an OAuth access token fresh, refreshing a minute before it runs out. Calls at once share one refresh. */
export class TokenSource {
  private pending?: Promise<string>;

  constructor(
    private tokens: { accessToken?: string; refreshToken?: string; expiresAt?: number },
    private readonly refresh: (refreshToken: string) => Promise<OAuthTokens>,
    private readonly onTokens?: (t: OAuthTokens) => Promise<void>,
  ) {}

  async token(): Promise<string> {
    const t = this.tokens;
    if (t.accessToken && (!t.expiresAt || Date.now() + 60_000 < t.expiresAt)) return t.accessToken;
    if (!t.refreshToken) throw new TrackerError("auth", t.accessToken ? "The authorization expired: authorize this connection again" : "Authorize this connection first");
    this.pending ??= (async () => {
      try {
        const n = await this.refresh(t.refreshToken!);
        this.tokens = { accessToken: n.accessToken, refreshToken: n.refreshToken ?? t.refreshToken, expiresAt: n.expiresAt };
        await this.onTokens?.({ accessToken: n.accessToken, refreshToken: this.tokens.refreshToken, expiresAt: n.expiresAt });
        return n.accessToken;
      } finally {
        this.pending = undefined;
      }
    })();
    return this.pending;
  }
}

/** Stored HTML (Confluence's storage format) as text: blocks become lines, tags go, entities are read. */
export function htmlText(s: string): string {
  let out = s
    .replace(/<\/(p|h[1-6])>/gi, "\n\n")
    .replace(/<br\s*\/?>/gi, "\n")
    .replace(/<\/(li|tr|div|pre)>/gi, "\n")
    .replace(/<li[^>]*>/gi, "- ")
    .replace(/<[^>]*>/g, "")
    .replace(/&nbsp;/g, " ")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#39;|&apos;/g, "'")
    .replace(/&amp;/g, "&");
  out = out
    .split("\n")
    .map((l) => l.replace(/[ \t]+$/, ""))
    .join("\n")
    .replace(/\n{3,}/g, "\n\n");
  return out.trim();
}

export function splitList(v: string): string[] {
  return v
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
}
