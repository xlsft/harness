/**
 * Single route table, used by nitro.config.ts (handlers) and by tests (in-process H3 app).
 * Paths of `handler` are relative to the package root.
 */
export interface RouteDef {
  route: string;
  method?: "get" | "post" | "put" | "patch" | "delete";
  handler: string;
}

export const routes: RouteDef[] = [
  { route: "/api/health", method: "get", handler: "./src/http/routes/health.ts" },

  // Connect services (proto/harness/**): DeviceAuthService, GatewayService, PromptService, SyncService,
  // SearchService, DecisionService
  { route: "/harness.auth.v1.DeviceAuthService/**", handler: "./src/http/routes/rpc.ts" },
  { route: "/harness.llm.v1.GatewayService/**", handler: "./src/http/routes/rpc.ts" },
  { route: "/harness.prompts.v1.PromptService/**", handler: "./src/http/routes/rpc.ts" },
  { route: "/harness.sync.v1.SyncService/**", handler: "./src/http/routes/rpc.ts" },
  { route: "/harness.search.v1.SearchService/**", handler: "./src/http/routes/rpc.ts" },
  { route: "/harness.decision.v1.DecisionService/**", handler: "./src/http/routes/rpc.ts" },

  // Better Auth (web sessions, admin plugin) and the device approval page stay plain HTTP
  { route: "/api/auth/**", handler: "./src/http/routes/better-auth.ts" },
  { route: "/device", handler: "./src/http/routes/device-page.ts" },

  // admin JSON API (web UI; not part of the client protocol)
  { route: "/api/admin/providers", handler: "./src/http/routes/admin-providers.ts" },
  { route: "/api/admin/providers/:id", handler: "./src/http/routes/admin-provider.ts" },
  { route: "/api/admin/providers/:id/discover", method: "post", handler: "./src/http/routes/admin-provider-discover.ts" },
  { route: "/api/admin/models", handler: "./src/http/routes/admin-models.ts" },
  { route: "/api/admin/models/:id", handler: "./src/http/routes/admin-model.ts" },
  { route: "/api/admin/slots", handler: "./src/http/routes/admin-slots.ts" },
  { route: "/api/admin/usage", method: "get", handler: "./src/http/routes/admin-usage.ts" },
  { route: "/api/admin/quotas", handler: "./src/http/routes/admin-quotas.ts" },
  { route: "/api/admin/decision", handler: "./src/http/routes/admin-decision.ts" },
  { route: "/api/admin/decision/log", method: "get", handler: "./src/http/routes/admin-decision-log.ts" },
];
