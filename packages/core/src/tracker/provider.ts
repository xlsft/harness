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

export type AuthMethodName = "token" | "password" | "oauth";

export interface Capabilities {
  provider: string;
  displayName: string;
  authMethods: AuthMethodName[];
  queryLanguage: string;
  queryExample: string;
  customFields: boolean;
  comments: boolean;
  knowledge: boolean;
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
  /** Present when capabilities().knowledge. */
  searchArticles?(query: string, base: string, limit: number): Promise<Article[]>;
  listArticles?(parentId: string, base: string, page: Page): Promise<{ articles: Article[]; hasMore: boolean }>;
  getArticle?(id: string): Promise<Article>;
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
