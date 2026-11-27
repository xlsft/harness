/**
 * The registry of tracker and knowledge providers on the server. Adding a provider is one module (src/tracker/<id>.ts exporting a
 * TrackerModule, src/knowledge/<id>.ts exporting a KnowledgeModule) and one line in a list here. The order is the order of the
 * rows in Settings. docs/design/tracker.md, "Writing a provider".
 */
import { CONFLUENCE } from "../knowledge/confluence.ts";
import { LINEAR_DOCS } from "../knowledge/linear-docs.ts";
import type { KnowledgeModule } from "../knowledge/provider.ts";
import { YOUTRACK_KB } from "../knowledge/youtrack-kb.ts";
import { JIRA } from "./jira.ts";
import { GITHUB } from "./github.ts";
import { GITLAB } from "./gitlab.ts";
import { LINEAR } from "./linear.ts";
import type { TrackerModule } from "./provider.ts";
import { YOUTRACK } from "./youtrack.ts";

/** Issue trackers. One line per provider. */
export const TRACKERS: TrackerModule[] = [YOUTRACK, JIRA, LINEAR, GITHUB, GITLAB];

/** Knowledge sources. One line per provider. */
export const KNOWLEDGE: KnowledgeModule[] = [YOUTRACK_KB, CONFLUENCE, LINEAR_DOCS];

export const trackerModule = (provider: string): TrackerModule | undefined => TRACKERS.find((m) => m.capabilities.provider === provider);
export const knowledgeModule = (provider: string): KnowledgeModule | undefined => KNOWLEDGE.find((m) => m.capabilities.provider === provider);
