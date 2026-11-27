/**
 * Linear's documents as a knowledge provider: no sign-in of its own, it borrows a Linear connection's. The narrowing is a Linear
 * project's name (`project: Handbook`); documents have no tree. https://linear.app/docs/project-documents
 */
import { LINEAR_API, openLinear } from "../tracker/linear.ts";
import type { KnowledgeCapabilities, KnowledgeModule } from "./provider.ts";

export const LINEAR_DOCS_CAPABILITIES: KnowledgeCapabilities = {
  provider: "linear-docs",
  displayName: "Linear documents",
  description: "Documents of a Linear workspace, read live, optionally of one project.",
  authMethods: [],
  docsUrl: "https://linear.app/docs/project-documents",
  logo: "linear",
  borrowFrom: ["linear"],
  queryLanguage: "Linear project",
  queryExample: "project: Handbook",
  defaultBaseUrl: LINEAR_API,
};

export const LINEAR_DOCS: KnowledgeModule = {
  capabilities: LINEAR_DOCS_CAPABILITIES,
  open: (o) => openLinear(o),
};
