/**
 * Knowledge providers on the server: the same shape as packages/client/tracker/knowledge.go. A knowledge source connection signs
 * in on its own or borrows the sign-in of a tracker connection whose provider is in borrowFrom. docs/design/tracker.md,
 * "Writing a provider".
 */
import type { Article, AuthHint, AuthMethodName, Identity, OAuthModule, OpenOptions, OptionField, Page } from "../tracker/provider.ts";

export type { Article } from "../tracker/provider.ts";

export interface KnowledgeCapabilities {
  /** Its id: "youtrack-kb", "confluence". */
  provider: string;
  displayName: string;
  /** One line on what it gives, in English. */
  description: string;
  /** Ways to sign in on its own. Empty when it can only borrow. */
  authMethods: AuthMethodName[];
  optionFields?: OptionField[];
  docsUrl?: string;
  logo?: string;
  /** Tracker providers whose connections it can borrow the sign-in of: same vendor, same site or organization. */
  borrowFrom: string[];
  queryLanguage?: string;
  queryExample?: string;
  baseUrlPlaceholder?: string;
  defaultBaseUrl?: string;
  authHints?: Partial<Record<AuthMethodName, AuthHint>>;
  /** Documents have children. */
  tree?: boolean;
}

/** One knowledge source connection, opened. Errors are TrackerError, classified the same way. */
export interface KnowledgeProvider {
  test(): Promise<Identity>;
  /** Searches with query, narrowed by base (the connection's query, in the provider's syntax). */
  searchArticles(query: string, base: string, limit: number): Promise<Article[]>;
  /** The children of parentId, or the top level narrowed by base. */
  listArticles(parentId: string, base: string, page: Page): Promise<{ articles: Article[]; hasMore: boolean }>;
  /** With content. Throws TrackerError("not_found"). */
  getArticle(id: string): Promise<Article>;
}

/**
 * What opening a knowledge provider takes. Borrowing, auth, login, oauthClientId, credentials and onOAuthTokens are the tracker
 * connection's, resolved now and never copied; options are its options under the knowledge connection's own.
 */
export interface KnowledgeOpenOptions extends OpenOptions {
  /** The tracker provider the sign-in is borrowed from; "" for its own. */
  borrowedFrom: string;
  /** The borrowed connection's address; "" for its own sign-in. */
  trackerBaseUrl: string;
}

/** One knowledge provider, as src/tracker/registry.ts lists it. */
export interface KnowledgeModule {
  capabilities: KnowledgeCapabilities;
  open(o: KnowledgeOpenOptions): KnowledgeProvider;
  oauth?: OAuthModule;
  normalizeUrl?(raw: string): string;
  validate?(baseUrl: string, options: Record<string, string>): void;
}
