/**
 * The YouTrack knowledge base as a knowledge provider of its own: the articles of an instance, usually with the sign-in of a
 * YouTrack tracker connection (borrowFrom), or with its own. The article methods are the YouTrack client's.
 */
import { normalizeYouTrackUrl, openYouTrack, YOUTRACK_AUTH_HINTS, YOUTRACK_OAUTH } from "../tracker/youtrack.ts";
import type { KnowledgeCapabilities, KnowledgeModule } from "./provider.ts";

export const YOUTRACK_KB_CAPABILITIES: KnowledgeCapabilities = {
  provider: "youtrack-kb",
  displayName: "YouTrack Knowledge Base",
  description: "Articles of a YouTrack instance, read live.",
  authMethods: ["token", "oauth", "password"],
  // https://www.jetbrains.com/help/youtrack/server/knowledge-base.html
  docsUrl: "https://www.jetbrains.com/help/youtrack/server/knowledge-base.html",
  logo: "youtrack",
  borrowFrom: ["youtrack"],
  queryLanguage: "YouTrack search",
  queryExample: "project: HELP",
  baseUrlPlaceholder: "https://youtrack.example.com",
  authHints: YOUTRACK_AUTH_HINTS,
  tree: true,
};

export const YOUTRACK_KB: KnowledgeModule = {
  capabilities: YOUTRACK_KB_CAPABILITIES,
  open: (o) => openYouTrack(o),
  oauth: YOUTRACK_OAUTH,
  normalizeUrl: normalizeYouTrackUrl,
};
