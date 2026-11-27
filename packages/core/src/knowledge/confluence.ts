/**
 * Confluence, Cloud and Data Center, as a knowledge provider: with its own sign-in, or with a Jira connection's on the same site
 * (borrowFrom). On Cloud it is the site's /wiki; on Data Center it has an address of its own. The article methods are the Jira
 * client's. https://developer.atlassian.com/cloud/confluence/rest/v1/intro/
 */
import { ATLASSIAN_AUTH_HINTS, ATLASSIAN_OAUTH, atlassian, openJira } from "../tracker/jira.ts";
import type { KnowledgeCapabilities, KnowledgeModule, KnowledgeOpenOptions } from "./provider.ts";

export const CONFLUENCE_CAPABILITIES: KnowledgeCapabilities = {
  provider: "confluence",
  displayName: "Confluence",
  description: "Pages of Confluence spaces, Cloud or Data Center.",
  authMethods: ["email_token", "token", "oauth"],
  docsUrl: "https://developer.atlassian.com/cloud/confluence/rest/v1/intro/",
  logo: "confluence",
  borrowFrom: ["jira"],
  queryLanguage: "CQL",
  queryExample: "space = DOCS",
  baseUrlPlaceholder: "https://your-site.atlassian.net/wiki",
  authHints: ATLASSIAN_AUTH_HINTS,
  tree: true,
};

const trim = (s: string) => s.trim().replace(/\/+$/, "");

/** The Jira site to sign in to and Confluence's own address ("" for the site's /wiki). */
export function confluenceSite(o: Pick<KnowledgeOpenOptions, "baseUrl" | "borrowedFrom" | "trackerBaseUrl">): { site: string; wiki: string } {
  const own = trim(o.baseUrl);
  const site = o.borrowedFrom && o.trackerBaseUrl ? trim(o.trackerBaseUrl) : own;
  if (atlassian.isCloud(new URL(site))) return { site: site.replace(/\/wiki$/, ""), wiki: "" };
  const wiki = (own && own !== site) || !o.borrowedFrom ? own : "";
  return { site, wiki };
}

export const CONFLUENCE: KnowledgeModule = {
  capabilities: CONFLUENCE_CAPABILITIES,
  open(o) {
    const { site, wiki } = confluenceSite(o);
    return openJira({ ...o, baseUrl: site }, wiki);
  },
  oauth: {
    authorizeUrl: (p) => ATLASSIAN_OAUTH.authorizeUrl({ ...p, baseUrl: p.baseUrl.replace(/\/+$/, "").replace(/\/wiki$/, "") }),
    exchangeCode: (p) => ATLASSIAN_OAUTH.exchangeCode({ ...p, baseUrl: p.baseUrl.replace(/\/+$/, "").replace(/\/wiki$/, "") }),
  },
};
