/**
 * The server's side of tracker providers: the same shape as packages/client/tracker/provider.go, because admin connections
 * make their requests from here and the server does not run Go. docs/design/tracker.md.
 */

export type TrackerErrorKind = "not_found" | "auth" | "forbidden" | "transient" | "other";

/** What a provider failed with, classified so the sync can decide. The message never carries a credential. */
export class TrackerError extends Error {
  constructor(
    readonly kind: TrackerErrorKind,
    message: string,
  ) {
    super(message);
    this.name = "TrackerError";
  }
}

export function trackerErrorKind(err: unknown): TrackerErrorKind {
  return err instanceof TrackerError ? err.kind : "other";
}

/**
 * How a connection signs in. "email_token": an account email in `login` and an API token (Atlassian Cloud basic auth). "vcs":
 * through the server's code host connection from Settings > VCS (option "vcs_connection_id"), see OpenOptions.vcs.
 */
export type AuthMethodName = "token" | "password" | "oauth" | "email_token" | "vcs";

export const AUTH_METHODS: readonly AuthMethodName[] = ["token", "password", "oauth", "email_token", "vcs"];

/** A provider-specific setting kept in a connection's options. Never a secret: options are stored and shown in the clear. */
export interface OptionField {
  /** Lower case letters, digits and underscores. */
  key: string;
  label: string;
  placeholder?: string;
  required?: boolean;
  /** When set, the only values taken; the form shows a choice. */
  allowedValues?: string[];
  help?: string;
  /** Used when the form leaves it empty. */
  defaultValue?: string;
}

/** What the form says for one sign-in method of one provider, in English. */
export interface AuthHint {
  help: string;
  secretLabel?: string;
  loginLabel?: string;
}

export interface Capabilities {
  provider: string;
  displayName: string;
  authMethods: AuthMethodName[];
  queryLanguage: string;
  queryExample: string;
  customFields: boolean;
  comments: boolean;
  optionFields?: OptionField[];
  /** Where the vendor documents the credentials the form asks for. */
  docsUrl?: string;
  /** Key in packages/ui/app/utils/providerLogos.ts. */
  logo?: string;
  baseUrlPlaceholder?: string;
  /** Used when the form leaves the address empty (a hosted-only service). Unset: the address is required. */
  defaultBaseUrl?: string;
  authHints?: Partial<Record<AuthMethodName, AuthHint>>;
}

/** What a connection signs in with, decrypted. Never logged, never returned by an RPC. */
export interface StoredCredentials {
  /** The token of "token" and "email_token". */
  token?: string;
  password?: string;
  clientSecret?: string;
  accessToken?: string;
  refreshToken?: string;
  /** Unix milliseconds. */
  expiresAt?: number;
}

export interface OAuthTokens {
  accessToken: string;
  refreshToken?: string;
  /** Unix milliseconds. */
  expiresAt: number;
}

/** A code host connection a "vcs" sign-in goes through. The token is fetched when needed and never stored by the provider. */
export interface VcsSignIn {
  type: "github" | "gitlab";
  baseUrl: string;
  token(): Promise<string>;
}

/** What opening a provider takes. Every request it makes goes through guardedFetch with allowPrivateNetwork. */
export interface OpenOptions {
  baseUrl: string;
  auth: AuthMethodName;
  login: string;
  oauthClientId: string;
  credentials: StoredCredentials;
  /** Checked against Capabilities.optionFields, defaults filled. */
  options: Record<string, string>;
  allowPrivateNetwork: boolean;
  retryDelaysMs?: number[];
  /** Store refreshed OAuth tokens. */
  onOAuthTokens?: (t: OAuthTokens) => Promise<void>;
  /** For "vcs": the code host connection named by options.vcs_connection_id. */
  vcs?: () => Promise<VcsSignIn>;
}

/** The provider-specific part of Authorization Code with PKCE. */
export interface OAuthModule {
  authorizeUrl(o: { baseUrl: string; clientId: string; redirectUri: string; state: string; challenge: string; allowPrivateNetwork: boolean; options: Record<string, string> }): Promise<string>;
  exchangeCode(o: { baseUrl: string; clientId: string; clientSecret?: string; code: string; redirectUri: string; verifier: string; allowPrivateNetwork: boolean; options: Record<string, string> }): Promise<OAuthTokens>;
}

/** One tracker provider, as src/tracker/registry.ts lists it. */
export interface TrackerModule {
  capabilities: Capabilities;
  open(o: OpenOptions): TrackerProvider;
  oauth?: OAuthModule;
  /** Normalizes an address the generic way when unset (trailing slashes, https:// for a bare host). */
  normalizeUrl?(raw: string): string;
  /** Checks what the generic checks can't; throws ApiError("invalid_request"). */
  validate?(baseUrl: string, options: Record<string, string>): void;
}

export interface Identity {
  login: string;
  displayName: string;
}

export interface IssueComment {
  author: string;
  text: string;
  createdAt: string;
}

export interface Issue {
  id: string;
  title: string;
  url: string;
  description: string;
  project: string;
  fields: { name: string; value: string }[];
  updatedAt: string;
  comments: IssueComment[];
}

export interface Field {
  name: string;
  kind: string;
  multi: boolean;
  values: string[];
}

export interface IssueDraft {
  project: string;
  title: string;
  description?: string;
  fields?: { name: string; value: string }[];
}

export interface IssueUpdate {
  title?: string;
  description?: string;
  fields?: { name: string; value: string }[];
}

export interface Article {
  id: string;
  title: string;
  url: string;
  content: string;
  project: string;
  updatedAt: string;
  parentId: string;
  hasChildren: boolean;
}

export interface ProviderProject {
  key: string;
  name: string;
  description: string;
  logo: string;
}

export interface Page {
  limit: number;
  offset?: number;
}

export interface TrackerProvider {
  capabilities(): Capabilities;
  test(): Promise<Identity>;
  search(query: string, page: Page): Promise<{ issues: Issue[]; hasMore: boolean }>;
  /** With description and recent comments. Throws TrackerError("not_found"). */
  get(id: string): Promise<Issue>;
  create(draft: IssueDraft): Promise<Issue>;
  update(id: string, update: IssueUpdate): Promise<void>;
  comment(id: string, text: string): Promise<void>;
  fields(project: string): Promise<Field[]>;
  projects(): Promise<ProviderProject[]>;
}

/** The words of a query in the provider's syntax that are plain text: filters such as `project: HELP` and #shortcuts are not. */
export function freeTextTerms(query: string): string[] {
  const terms: string[] = [];
  const rest = query.replace(/"([^"]*)"/g, (_, phrase: string) => {
    if (phrase.trim()) terms.push(phrase.trim().toLowerCase());
    return " ";
  });
  const words = rest.replace(/\{[^}]*\}/g, " ").split(/\s+/).filter(Boolean);
  for (let i = 0; i < words.length; i++) {
    const w = words[i]!;
    if (w.endsWith(":")) {
      i++;
      continue;
    }
    if (w.includes(":") || w.startsWith("#") || w.startsWith("-")) continue;
    terms.push(w.toLowerCase());
  }
  return terms;
}

/** Up to `max` lines holding a term, or the start of the text when none does. */
export function snippetOf(content: string, terms: string[], max = 3): string {
  const lines = content.split("\n");
  const hit = lines.filter((l) => terms.some((t) => l.toLowerCase().includes(t))).slice(0, max);
  const text = (hit.length ? hit : lines.slice(0, max)).join("\n").trim();
  return text.length > 1200 ? `${text.slice(0, 1200)}…` : text;
}
