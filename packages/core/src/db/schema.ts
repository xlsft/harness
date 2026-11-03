import { bigint, bigserial, boolean, doublePrecision, index, integer, jsonb, pgTable, primaryKey, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";

const ts = (name: string) => timestamp(name, { withTimezone: true, mode: "date" });
/** int8 read back as a JS number: safe up to 2^53, far beyond any version or seq we will reach. */
const int8 = (name: string) => bigint(name, { mode: "number" });

// ---------------------------------------------------------------------------
// Better Auth tables (core + admin plugin). JS keys must match Better Auth field names.
// ---------------------------------------------------------------------------

export const user = pgTable("user", {
  id: text("id").primaryKey(),
  name: text("name").notNull(),
  email: text("email").notNull().unique(),
  emailVerified: boolean("email_verified").notNull().default(false),
  image: text("image"),
  createdAt: ts("created_at").notNull(),
  updatedAt: ts("updated_at").notNull(),
  role: text("role"),
  banned: boolean("banned").default(false),
  banReason: text("ban_reason"),
  banExpires: ts("ban_expires"),
  username: text("username").unique(),
  displayUsername: text("display_username"),
});

export const session = pgTable(
  "session",
  {
    id: text("id").primaryKey(),
    expiresAt: ts("expires_at").notNull(),
    token: text("token").notNull().unique(),
    createdAt: ts("created_at").notNull(),
    updatedAt: ts("updated_at").notNull(),
    ipAddress: text("ip_address"),
    userAgent: text("user_agent"),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    impersonatedBy: text("impersonated_by"),
  },
  (t) => [index("session_user_idx").on(t.userId)],
);

export const account = pgTable(
  "account",
  {
    id: text("id").primaryKey(),
    accountId: text("account_id").notNull(),
    providerId: text("provider_id").notNull(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    accessToken: text("access_token"),
    refreshToken: text("refresh_token"),
    idToken: text("id_token"),
    accessTokenExpiresAt: ts("access_token_expires_at"),
    refreshTokenExpiresAt: ts("refresh_token_expires_at"),
    scope: text("scope"),
    password: text("password"),
    createdAt: ts("created_at").notNull(),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [index("account_user_idx").on(t.userId)],
);

export const verification = pgTable("verification", {
  id: text("id").primaryKey(),
  identifier: text("identifier").notNull(),
  value: text("value").notNull(),
  expiresAt: ts("expires_at").notNull(),
  createdAt: ts("created_at").notNull(),
  updatedAt: ts("updated_at").notNull(),
});

// ---------------------------------------------------------------------------
// Devices and tokens (docs/protocol/auth.md)
// ---------------------------------------------------------------------------

/** Pending device-flow authorization requests. Codes are stored hashed. */
export const deviceCode = pgTable("device_code", {
  deviceCodeHash: text("device_code_hash").primaryKey(),
  userCode: text("user_code").notNull().unique(),
  clientName: text("client_name"),
  deviceName: text("device_name"),
  os: text("os"),
  arch: text("arch"),
  /** pending | approved | denied | consumed */
  status: text("status").notNull().default("pending"),
  userId: text("user_id").references(() => user.id, { onDelete: "cascade" }),
  intervalSec: integer("interval_sec").notNull(),
  lastPolledAt: ts("last_polled_at"),
  expiresAt: ts("expires_at").notNull(),
  createdAt: ts("created_at").notNull(),
});

export const device = pgTable(
  "device",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    clientName: text("client_name"),
    os: text("os"),
    arch: text("arch"),
    userAgent: text("user_agent"),
    tokenHash: text("token_hash").notNull().unique(),
    createdAt: ts("created_at").notNull(),
    lastSeenAt: ts("last_seen_at"),
    revokedAt: ts("revoked_at"),
  },
  (t) => [index("device_user_idx").on(t.userId)],
);

export const accessToken = pgTable(
  "access_token",
  {
    tokenHash: text("token_hash").primaryKey(),
    deviceId: text("device_id")
      .notNull()
      .references(() => device.id, { onDelete: "cascade" }),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    expiresAt: ts("expires_at").notNull(),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [index("access_token_device_idx").on(t.deviceId)],
);

// ---------------------------------------------------------------------------
// Providers, model catalog, settings
// ---------------------------------------------------------------------------

export const provider = pgTable(
  "provider",
  {
    id: text("id").primaryKey(),
    /** Null: a global provider (admin-managed, usable by everyone). Set: a personal provider only its owner sees and uses. */
    ownerUserId: text("owner_user_id").references(() => user.id, { onDelete: "cascade" }),
    /** "llm" (gateway models), "classifier" (auto-mode decision services, global only) or "search" (web search APIs). */
    kind: text("kind").notNull().default("llm"),
    /** llm: anthropic | openai-chat | openai-responses; classifier: decision-model | decision-http; search: see services/presets.ts */
    type: text("type").notNull(),
    /** The preset it was connected from (services/presets.ts); null for a custom provider. */
    presetId: text("preset_id"),
    displayName: text("display_name"),
    baseUrl: text("base_url").notNull(),
    /** AES-256-GCM, see lib/crypto.ts. Null when the provider needs no key (e.g. local Ollama). */
    apiKeyEnc: text("api_key_enc"),
    apiKeyHint: text("api_key_hint"),
    /** Encrypted JSON object of extra headers. */
    headersEnc: text("headers_enc"),
    allowPrivateNetwork: boolean("allow_private_network").notNull().default(false),
    enabled: boolean("enabled").notNull().default(true),
    /** Search providers: JSON object of options (result count, safe search, country, language), plain text values. */
    options: jsonb("options"),
    createdAt: ts("created_at").notNull(),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [index("provider_owner_idx").on(t.ownerUserId)],
);

export const model = pgTable("model", {
  /** Alias the client sends in `model`. Personal models are `<provider id>/<upstream model>` (services/models.ts). */
  id: text("id").primaryKey(),
  displayName: text("display_name").notNull(),
  providerId: text("provider_id")
    .notNull()
    // Deleting a global provider that models still use is refused in services/providers.ts; the cascade
    // removes a personal provider's models with it (also when its owner is deleted).
    .references(() => provider.id, { onDelete: "cascade" }),
  upstreamModel: text("upstream_model").notNull(),
  contextWindow: integer("context_window").notNull(),
  maxOutputTokens: integer("max_output_tokens").notNull(),
  /** JSON: ModelCapabilities */
  capabilities: jsonb("capabilities").notNull(),
  /** JSON: ModelPricing | null */
  pricing: jsonb("pricing"),
  /** JSON: string[] of model aliases tried on overloaded/unavailable. */
  fallbacks: jsonb("fallbacks").notNull().default([]),
  /** JSON object merged into the upstream request body (e.g. vLLM/Qwen switches). */
  extraBody: jsonb("extra_body"),
  /** JSON: string[] of roles allowed to see the model; null = everyone. */
  roles: jsonb("roles"),
  enabled: boolean("enabled").notNull().default(true),
  sortOrder: integer("sort_order").notNull().default(0),
  createdAt: ts("created_at").notNull(),
  updatedAt: ts("updated_at").notNull(),
});

/** Features an upstream rejected for a model (capability negotiation memory). */
export const modelNegotiation = pgTable(
  "model_negotiation",
  {
    modelId: text("model_id")
      .notNull()
      .references(() => model.id, { onDelete: "cascade" }),
    feature: text("feature").notNull(),
    reason: text("reason"),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.modelId, t.feature] })],
);

export const setting = pgTable("setting", {
  key: text("key").primaryKey(),
  value: jsonb("value").notNull(),
  updatedAt: ts("updated_at").notNull(),
});

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

export const usageRecord = pgTable(
  "usage_record",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    deviceId: text("device_id"),
    modelId: text("model_id").notNull(),
    providerId: text("provider_id"),
    upstreamModel: text("upstream_model"),
    requestClass: text("request_class"),
    sessionId: text("session_id"),
    agentId: text("agent_id"),
    inputTokens: integer("input_tokens").notNull().default(0),
    outputTokens: integer("output_tokens").notNull().default(0),
    cacheReadTokens: integer("cache_read_tokens").notNull().default(0),
    cacheWriteTokens: integer("cache_write_tokens").notNull().default(0),
    costUsd: doublePrecision("cost_usd").notNull().default(0),
    /**
     * The request was billed at a catalog price (a priced model on a metered provider). False: it counts
     * tokens only (no price, or a subscription / coding plan provider), so its cost is unknown, not 0.
     * Null on records from before the column (services/usage.ts infers it).
     */
    priced: boolean("priced"),
    retries: integer("retries").notNull().default(0),
    /** ok | error | aborted */
    status: text("status").notNull(),
    stopReason: text("stop_reason"),
    errorType: text("error_type"),
    durationMs: integer("duration_ms"),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [index("usage_user_created_idx").on(t.userId, t.createdAt), index("usage_session_idx").on(t.sessionId)],
);

// ---------------------------------------------------------------------------
// Sync (proto/harness/sync/v1): versioned documents and session transcripts
// ---------------------------------------------------------------------------

/**
 * Named monotonically increasing counters. "documents" is the global sync version counter
 * (see services/sync.ts nextSyncVersion). A row (not a sequence) on purpose: the increment is part of
 * the writing transaction, so the counter only ever shows committed versions and its row lock orders
 * concurrent writers.
 */
export const syncCounter = pgTable("sync_counter", {
  name: text("name").primaryKey(),
  value: int8("value").notNull(),
});

/** Current state of every document; deleted documents stay as tombstones (deleted_at set). */
export const syncDocument = pgTable(
  "sync_document",
  {
    id: text("id").primaryKey(),
    /** usr_... for user and project scope; "" for org scope (keeps the unique key non-null). */
    ownerId: text("owner_id").notNull(),
    /** harness.common.v1.Scope */
    scope: integer("scope").notNull(),
    /** "" unless scope = project. */
    projectKey: text("project_key").notNull().default(""),
    /** harness.sync.v1.DocumentKind */
    kind: integer("kind").notNull(),
    key: text("key").notNull(),
    /** Global sync version of the last write (or of the delete). */
    version: int8("version").notNull(),
    content: text("content").notNull().default(""),
    /** JSON: {path, data (base64), executable}[] */
    files: jsonb("files").notNull().default([]),
    contentHash: text("content_hash").notNull(),
    source: text("source").notNull().default(""),
    /** JSON: string[]; org scope only. */
    targetRoles: jsonb("target_roles").notNull().default([]),
    createdAt: ts("created_at").notNull(),
    updatedAt: ts("updated_at").notNull(),
    updatedBy: text("updated_by").notNull(),
    deletedAt: ts("deleted_at"),
  },
  (t) => [
    uniqueIndex("sync_document_key_idx").on(t.ownerId, t.scope, t.projectKey, t.kind, t.key),
    index("sync_document_version_idx").on(t.version),
  ],
);

/** Version history of documents (every write and delete). */
export const syncDocumentRevision = pgTable(
  "sync_document_revision",
  {
    documentId: text("document_id").notNull(),
    version: int8("version").notNull(),
    content: text("content").notNull(),
    files: jsonb("files").notNull(),
    contentHash: text("content_hash").notNull(),
    deleted: boolean("deleted").notNull().default(false),
    updatedBy: text("updated_by").notNull(),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.documentId, t.version] })],
);

/** Session metadata (SessionMetadata). `info` is the protobuf JSON of harness.agent.v1.SessionInfo. */
export const syncSession = pgTable(
  "sync_session",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    info: jsonb("info").notNull(),
    // Columns copied out of `info` for filtering and search.
    title: text("title").notNull().default(""),
    cwd: text("cwd").notNull().default(""),
    repo: text("repo").notNull().default(""),
    deviceId: text("device_id").notNull().default(""),
    status: integer("status").notNull().default(0),
    archived: boolean("archived").notNull().default(false),
    promptBundleVersion: text("prompt_bundle_version").notNull().default(""),
    lastSeq: int8("last_seq").notNull().default(0),
    createdAt: ts("created_at").notNull(),
    lastActivityAt: ts("last_activity_at").notNull(),
  },
  (t) => [index("sync_session_user_activity_idx").on(t.userId, t.lastActivityAt)],
);

/**
 * Durable transcript: CC-format JSONL lines, byte-exact. `seq` is one counter per session shared by the
 * main thread and its subagents, so each thread's entries are increasing (not dense) when paged by agent_id.
 */
export const syncSessionEntry = pgTable(
  "sync_session_entry",
  {
    sessionId: text("session_id")
      .notNull()
      .references(() => syncSession.id, { onDelete: "cascade" }),
    seq: int8("seq").notNull(),
    uuid: text("uuid").notNull(),
    /** Subagent id; "" for the main thread. Subagent transcripts are paged separately. */
    agentId: text("agent_id").notNull().default(""),
    parentUuid: text("parent_uuid").notNull().default(""),
    type: text("type").notNull().default(""),
    timestamp: text("timestamp").notNull().default(""),
    entryJson: text("entry_json").notNull(),
    /** Text content extracted for search. */
    searchText: text("search_text").notNull().default(""),
  },
  (t) => [primaryKey({ columns: [t.sessionId, t.seq] }), uniqueIndex("sync_session_entry_uuid_idx").on(t.sessionId, t.agentId, t.uuid)],
);

// ---------------------------------------------------------------------------
// Decisions (proto/harness/decision/v1): auto-mode provider chain and audit log
// ---------------------------------------------------------------------------

/**
 * Provider chain configuration of DecisionService (admin JSON API /api/admin/decision). One row, id
 * "default". `config` is the stored form of src/decision/config.ts DecisionConfig; webhook secrets inside
 * it are encrypted (lib/crypto.ts).
 */
export const decisionConfig = pgTable("decision_config", {
  id: text("id").primaryKey(),
  config: jsonb("config").notNull(),
  updatedAt: ts("updated_at").notNull(),
  updatedBy: text("updated_by"),
});

/** Every Decide call: who asked, what was judged (hash + truncated preview) and what came out. */
export const decisionLog = pgTable(
  "decision_log",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    deviceId: text("device_id"),
    sessionId: text("session_id").notNull().default(""),
    toolName: text("tool_name").notNull(),
    /** sha256 of input_json. */
    inputHash: text("input_hash").notNull(),
    inputPreview: text("input_preview").notNull().default(""),
    /** allow | deny | ask */
    decision: text("decision").notNull(),
    /** Provider id that decided, or "none". */
    provider: text("provider").notNull(),
    /** JSON: RiskCategory names. */
    categories: jsonb("categories").notNull().default([]),
    reason: text("reason").notNull().default(""),
    confidence: doublePrecision("confidence"),
    latencyMs: integer("latency_ms").notNull(),
    costUsd: doublePrecision("cost_usd").notNull().default(0),
    cached: boolean("cached").notNull().default(false),
    /** Set when a provider failed and on_error turned the failure into the answer or a fall-through. */
    error: text("error"),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [
    index("decision_log_session_idx").on(t.sessionId, t.createdAt),
    index("decision_log_user_idx").on(t.userId, t.createdAt),
    index("decision_log_created_idx").on(t.createdAt),
  ],
);

// ---------------------------------------------------------------------------
// MCP (proto/harness/mcp/v1): registry, secrets, OAuth, device status, tool catalog
// ---------------------------------------------------------------------------

/**
 * MCP server registry. `version` lives in the global sync version space (services/sync.ts); every write
 * also writes a DOCUMENT_KIND_MCP_SERVER notification row into sync_document in the same transaction.
 */
export const mcpServer = pgTable(
  "mcp_server",
  {
    id: text("id").primaryKey(),
    /** usr_... for user and project scope; "" for org scope. */
    ownerId: text("owner_id").notNull(),
    /** harness.common.v1.Scope */
    scope: integer("scope").notNull(),
    projectKey: text("project_key").notNull().default(""),
    name: text("name").notNull(),
    /** harness.mcp.v1.Transport */
    transport: integer("transport").notNull(),
    enabled: boolean("enabled").notNull().default(true),
    alwaysLoad: boolean("always_load").notNull().default(false),
    /** JSON: StoredConfig (src/mcp/config.ts). Never contains secret values. */
    config: jsonb("config").notNull(),
    /** JSON: string[]; org scope only. */
    targetRoles: jsonb("target_roles").notNull().default([]),
    /** Admin-only switch (not part of the RPC contract): lets the proxy reach private / loopback addresses. */
    allowPrivateNetwork: boolean("allow_private_network").notNull().default(false),
    version: int8("version").notNull(),
    configHash: text("config_hash").notNull(),
    createdAt: ts("created_at").notNull(),
    updatedAt: ts("updated_at").notNull(),
    updatedBy: text("updated_by").notNull(),
  },
  (t) => [uniqueIndex("mcp_server_name_idx").on(t.ownerId, t.scope, t.projectKey, t.name)],
);

/** Write-only secrets of a server; values AES-256-GCM encrypted with AAD "<server id>:secret:<name>". */
export const mcpSecret = pgTable(
  "mcp_secret",
  {
    serverId: text("server_id")
      .notNull()
      .references(() => mcpServer.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    valueEnc: text("value_enc").notNull(),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.serverId, t.name] })],
);

/**
 * OAuth client per server, authorization server and redirect URI: the dynamic client registration result
 * (or the configured client id) plus the discovered metadata needed for later refreshes.
 */
export const mcpOauthClient = pgTable(
  "mcp_oauth_client",
  {
    serverId: text("server_id")
      .notNull()
      .references(() => mcpServer.id, { onDelete: "cascade" }),
    /** Authorization server URL. */
    issuer: text("issuer").notNull(),
    redirectUri: text("redirect_uri").notNull(),
    /** Encrypted JSON OAuthClientInformationFull. */
    clientEnc: text("client_enc").notNull(),
    /** JSON AuthorizationServerMetadata (or null when discovery found none). */
    metadata: jsonb("metadata"),
    dynamic: boolean("dynamic").notNull().default(false),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.serverId, t.issuer, t.redirectUri] })],
);

/** Pending and finished OAuth authorizations (StartOAuth -> /api/mcp/oauth/callback). */
export const mcpOauthFlow = pgTable(
  "mcp_oauth_flow",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    serverId: text("server_id")
      .notNull()
      .references(() => mcpServer.id, { onDelete: "cascade" }),
    /** sha256 of the `state` parameter. */
    stateHash: text("state_hash").notNull().unique(),
    /** Encrypted JSON FlowData (src/mcp/oauth.ts): PKCE verifier, issuer, redirect URI, resource, scopes. */
    dataEnc: text("data_enc").notNull(),
    returnUrl: text("return_url").notNull().default(""),
    /** pending | completed | failed */
    status: text("status").notNull().default("pending"),
    error: text("error").notNull().default(""),
    expiresAt: ts("expires_at").notNull(),
    createdAt: ts("created_at").notNull(),
    completedAt: ts("completed_at"),
  },
  (t) => [index("mcp_oauth_flow_user_idx").on(t.userId)],
);

/** Per-user authorization of a remote server: encrypted tokens and the last known state. */
export const mcpAuth = pgTable(
  "mcp_auth",
  {
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    serverId: text("server_id")
      .notNull()
      .references(() => mcpServer.id, { onDelete: "cascade" }),
    /** connected | needs_auth | error */
    state: text("state").notNull(),
    message: text("message").notNull().default(""),
    /** Encrypted JSON OAuthTokens (AAD "<user id>:<server id>"); null when not connected. */
    tokensEnc: text("tokens_enc"),
    issuer: text("issuer").notNull().default(""),
    redirectUri: text("redirect_uri").notNull().default(""),
    /** RFC 8707 resource the tokens were issued for (sent again on refresh). */
    resource: text("resource").notNull().default(""),
    /** remote.url (as configured, unsubstituted) at authorization time; a URL change requires a new authorization. */
    serverUrl: text("server_url").notNull().default(""),
    /** JSON string[]: scopes asked for / granted. */
    requestedScopes: jsonb("requested_scopes").notNull().default([]),
    grantedScopes: jsonb("granted_scopes").notNull().default([]),
    account: text("account").notNull().default(""),
    expiresAt: ts("expires_at"),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.userId, t.serverId] })],
);

/** Per-device availability / approval report (ReportDeviceStatus). */
export const mcpDeviceStatus = pgTable(
  "mcp_device_status",
  {
    deviceId: text("device_id")
      .notNull()
      .references(() => device.id, { onDelete: "cascade" }),
    serverId: text("server_id")
      .notNull()
      .references(() => mcpServer.id, { onDelete: "cascade" }),
    userId: text("user_id").notNull(),
    configHash: text("config_hash").notNull().default(""),
    /** harness.mcp.v1.Availability */
    availability: integer("availability").notNull().default(0),
    reason: text("reason").notNull().default(""),
    /** harness.mcp.v1.ApprovalState */
    approval: integer("approval").notNull().default(0),
    lastError: text("last_error").notNull().default(""),
    /** JSON ToolData[] discovered by the last launch. */
    tools: jsonb("tools").notNull().default([]),
    /** When `tools` was last replaced by a non-empty list. */
    toolsReportedAt: ts("tools_reported_at"),
    reportedAt: ts("reported_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.deviceId, t.serverId] }), index("mcp_device_status_server_idx").on(t.serverId)],
);

/** Tool lists of remote servers fetched through the proxy, per server and user (a list can depend on the grant). */
export const mcpToolCache = pgTable(
  "mcp_tool_cache",
  {
    serverId: text("server_id")
      .notNull()
      .references(() => mcpServer.id, { onDelete: "cascade" }),
    userId: text("user_id").notNull(),
    /** config_hash the list was fetched with. */
    configHash: text("config_hash").notNull(),
    /** JSON ToolData[] */
    tools: jsonb("tools").notNull(),
    fetchedAt: ts("fetched_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.serverId, t.userId] })],
);

/** Frozen tool catalog snapshots (GetToolCatalog). Kept >= 7 days after their last use. */
export const mcpCatalogSnapshot = pgTable(
  "mcp_catalog_snapshot",
  {
    version: text("version").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    projectKey: text("project_key").notNull().default(""),
    /** JSON CatalogServerData[] */
    servers: jsonb("servers").notNull(),
    createdAt: ts("created_at").notNull(),
    lastUsedAt: ts("last_used_at").notNull(),
  },
  (t) => [index("mcp_catalog_snapshot_used_idx").on(t.lastUsedAt)],
);

/** Audit log of sensitive MCP operations (stdio launches, tool calls, authorizations). Never stores secret values. */
export const mcpAudit = pgTable(
  "mcp_audit",
  {
    id: text("id").primaryKey(),
    userId: text("user_id").notNull(),
    deviceId: text("device_id"),
    serverId: text("server_id").notNull(),
    /** stdio_launch | call_tool | oauth_completed | oauth_revoked */
    action: text("action").notNull(),
    configHash: text("config_hash").notNull().default(""),
    /** JSON detail (tool name, session id, outcome, ...). Never secret values or tool arguments. */
    detail: jsonb("detail").notNull().default({}),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [index("mcp_audit_server_idx").on(t.serverId, t.createdAt), index("mcp_audit_user_idx").on(t.userId, t.createdAt)],
);

// ---------------------------------------------------------------------------
// Code hosts (proto/harness/vcs/v1): connections, per-user tokens, events, PR state, session links
// ---------------------------------------------------------------------------

/** Server provider connections (GitHub App, GitLab). Secrets are one encrypted JSON blob (AAD = id). */
export const vcsConnection = pgTable("vcs_connection", {
  id: text("id").primaryKey(),
  /** harness.vcs.v1.ProviderType (GITHUB or GITLAB). */
  type: integer("type").notNull(),
  displayName: text("display_name").notNull().default(""),
  /** Normalized origin + path, no trailing slash (https://github.com, a GHES URL, a GitLab URL). */
  baseUrl: text("base_url").notNull(),
  appId: text("app_id").notNull().default(""),
  appSlug: text("app_slug").notNull().default(""),
  /** OAuth client id (GitHub App client id / GitLab application id); public. */
  clientId: text("client_id").notNull().default(""),
  /** Encrypted JSON {privateKeyPem, clientSecret, webhookSecret, accessToken}; "" when none. */
  secretsEnc: text("secrets_enc").notNull().default(""),
  allowPrivateNetwork: boolean("allow_private_network").notNull().default(false),
  healthy: boolean("healthy").notNull().default(false),
  lastError: text("last_error").notNull().default(""),
  lastCheckedAt: ts("last_checked_at"),
  lastWebhookAt: ts("last_webhook_at"),
  createdAt: ts("created_at").notNull(),
  updatedAt: ts("updated_at").notNull(),
  updatedBy: text("updated_by").notNull().default(""),
});

/** GitHub App installations (from installation webhooks and lookups); maps an owner to an installation. */
export const vcsInstallation = pgTable(
  "vcs_installation",
  {
    connectionId: text("connection_id")
      .notNull()
      .references(() => vcsConnection.id, { onDelete: "cascade" }),
    installationId: int8("installation_id").notNull(),
    /** Lowercased account login (user or organization). */
    accountLogin: text("account_login").notNull(),
    accountType: text("account_type").notNull().default(""),
    suspended: boolean("suspended").notNull().default(false),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.connectionId, t.installationId] }), index("vcs_installation_account_idx").on(t.connectionId, t.accountLogin)],
);

/** Pending GitHub App manifest registrations (GetGitHubAppManifest -> CompleteGitHubAppManifest). */
export const vcsManifestFlow = pgTable("vcs_manifest_flow", {
  id: text("id").primaryKey(),
  userId: text("user_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  stateHash: text("state_hash").notNull().unique(),
  baseUrl: text("base_url").notNull(),
  organization: text("organization").notNull().default(""),
  appName: text("app_name").notNull().default(""),
  allowPrivateNetwork: boolean("allow_private_network").notNull().default(false),
  /** pending | completed | failed */
  status: text("status").notNull().default("pending"),
  connectionId: text("connection_id").notNull().default(""),
  error: text("error").notNull().default(""),
  expiresAt: ts("expires_at").notNull(),
  createdAt: ts("created_at").notNull(),
});

/** Per-user OAuth flows (StartUserAuth -> /api/vcs/oauth/callback). */
export const vcsOauthFlow = pgTable(
  "vcs_oauth_flow",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    connectionId: text("connection_id")
      .notNull()
      .references(() => vcsConnection.id, { onDelete: "cascade" }),
    stateHash: text("state_hash").notNull().unique(),
    /** Encrypted JSON {codeVerifier, redirectUri}. */
    dataEnc: text("data_enc").notNull(),
    /** pending | completed | failed */
    status: text("status").notNull().default("pending"),
    error: text("error").notNull().default(""),
    expiresAt: ts("expires_at").notNull(),
    createdAt: ts("created_at").notNull(),
    completedAt: ts("completed_at"),
  },
  (t) => [index("vcs_oauth_flow_user_idx").on(t.userId)],
);

/** Per-user tokens on a connection (encrypted JSON, AAD "<user>:<connection>:tokens"). */
export const vcsUserToken = pgTable(
  "vcs_user_token",
  {
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    connectionId: text("connection_id")
      .notNull()
      .references(() => vcsConnection.id, { onDelete: "cascade" }),
    /** connected | error */
    state: text("state").notNull(),
    error: text("error").notNull().default(""),
    tokensEnc: text("tokens_enc"),
    /** Login on the code host. */
    login: text("login").notNull().default(""),
    expiresAt: ts("expires_at"),
    refreshExpiresAt: ts("refresh_expires_at"),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.userId, t.connectionId] })],
);

/** VcsEvents (webhooks, device snapshots), kept 7 days. The public id is "vev_<seq>". */
export const vcsEvent = pgTable(
  "vcs_event",
  {
    seq: bigserial("seq", { mode: "number" }).primaryKey(),
    /** harness.vcs.v1.VcsEvent.Kind */
    kind: integer("kind").notNull(),
    /** harness.vcs.v1.ProviderType that observed it. */
    source: integer("source").notNull(),
    connectionId: text("connection_id").notNull().default(""),
    deliveryId: text("delivery_id").notNull().default(""),
    repositoryKey: text("repository_key").notNull(),
    prNumber: int8("pr_number").notNull().default(0),
    headBranch: text("head_branch").notNull().default(""),
    /** JSON PrData (src/vcs/types.ts) or null. */
    pullRequest: jsonb("pull_request"),
    /** JSON {sessionId, userId}[] of linked sessions. */
    sessions: jsonb("sessions").notNull().default([]),
    /** usr_... that reported it (device snapshots); "" for webhooks. */
    actorUserId: text("actor_user_id").notNull().default(""),
    detail: jsonb("detail").notNull().default({}),
    occurredAt: ts("occurred_at").notNull(),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [index("vcs_event_repo_idx").on(t.repositoryKey, t.seq), index("vcs_event_created_idx").on(t.createdAt)],
);

/** Processed webhook deliveries (idempotency), kept 7 days. key = "<connection id>:<delivery id>". */
export const vcsDelivery = pgTable("vcs_delivery", {
  key: text("key").primaryKey(),
  createdAt: ts("created_at").notNull(),
});

/** Last known state of every PR/MR the server heard about (webhooks, API reads, device snapshots). */
export const vcsPullRequest = pgTable(
  "vcs_pull_request",
  {
    repositoryKey: text("repository_key").notNull(),
    number: int8("number").notNull(),
    connectionId: text("connection_id").notNull().default(""),
    /** harness.vcs.v1.ProviderType of the last writer. */
    source: integer("source").notNull(),
    headBranch: text("head_branch").notNull().default(""),
    headSha: text("head_sha").notNull().default(""),
    baseBranch: text("base_branch").notNull().default(""),
    /** JSON PrData. */
    data: jsonb("data").notNull(),
    /** JSON CheckData[] of the head commit (device snapshots, webhooks). */
    checks: jsonb("checks").notNull().default([]),
    reportedBy: text("reported_by").notNull().default(""),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.repositoryKey, t.number] }), index("vcs_pull_request_branch_idx").on(t.repositoryKey, t.headBranch)],
);

/** Explicit session links (LinkSession) and auto-fix flags (SetAutoFix). */
export const vcsSessionLink = pgTable("vcs_session_link", {
  sessionId: text("session_id")
    .primaryKey()
    .references(() => syncSession.id, { onDelete: "cascade" }),
  userId: text("user_id").notNull(),
  /** "" when only auto-fix flags are set (the link then comes from synced session metadata). */
  repositoryKey: text("repository_key").notNull().default(""),
  branch: text("branch").notNull().default(""),
  prNumber: int8("pr_number").notNull().default(0),
  autoFixCi: boolean("auto_fix_ci").notNull().default(false),
  autoFixReviews: boolean("auto_fix_reviews").notNull().default(false),
  autoFixConflicts: boolean("auto_fix_conflicts").notNull().default(false),
  updatedAt: ts("updated_at").notNull(),
});

/** Auto-fix work for a session (failed checks with a log excerpt, review comments, conflicts). */
export const vcsAutofixJob = pgTable(
  "vcs_autofix_job",
  {
    id: text("id").primaryKey(),
    sessionId: text("session_id")
      .notNull()
      .references(() => syncSession.id, { onDelete: "cascade" }),
    userId: text("user_id").notNull(),
    /** seq of the VcsEvent that triggered it. */
    eventSeq: int8("event_seq").notNull(),
    /** harness.vcs.v1.VcsEvent.Kind of the trigger. */
    kind: integer("kind").notNull(),
    repositoryKey: text("repository_key").notNull(),
    prNumber: int8("pr_number").notNull().default(0),
    headSha: text("head_sha").notNull().default(""),
    /** pending | delivered | failed */
    status: text("status").notNull().default("pending"),
    /** JSON {failed_checks, log_excerpts, comment_ids, comments, base_branch, ...}. */
    payload: jsonb("payload").notNull().default({}),
    createdAt: ts("created_at").notNull(),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [index("vcs_autofix_job_session_idx").on(t.sessionId, t.createdAt)],
);

/** Device providers (gh, glab) a client reported (ReportDeviceProviders). */
export const vcsDeviceProvider = pgTable(
  "vcs_device_provider",
  {
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    /** dev_... ("" for web sessions). */
    deviceId: text("device_id").notNull().default(""),
    /** harness.vcs.v1.ProviderType (GH or GLAB). */
    type: integer("type").notNull(),
    version: text("version").notNull().default(""),
    /** JSON {host, login}[] */
    hosts: jsonb("hosts").notNull().default([]),
    reportedAt: ts("reported_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.userId, t.deviceId, t.type] })],
);

// ---------------------------------------------------------------------------
// Relay (proto/harness/relay/v1): remote control hosts, live sessions, durable events, commands
// ---------------------------------------------------------------------------

/** A client process registered as a relay host (RegisterHost). One row per (device, instance id). */
export const relayHost = pgTable(
  "relay_host",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    deviceId: text("device_id")
      .notNull()
      .references(() => device.id, { onDelete: "cascade" }),
    instanceId: text("instance_id").notNull(),
    clientVersion: text("client_version").notNull().default(""),
    daemon: boolean("daemon").notNull().default(false),
    allowedRoots: jsonb("allowed_roots").notNull().default([]),
    tunnels: boolean("tunnels").notNull().default(false),
    pty: boolean("pty").notNull().default(false),
    lastSeenAt: ts("last_seen_at").notNull(),
    lastLocalActivityAt: ts("last_local_activity_at"),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [uniqueIndex("relay_host_instance_idx").on(t.deviceId, t.instanceId), index("relay_host_user_idx").on(t.userId)],
);

/**
 * A remote-controllable session and its current owner. `epoch` grows with every RegisterSession; the host of
 * the newest epoch owns the session. `acked_seq` is the highest contiguous durable seq stored in relay_event.
 * `info` is the protobuf JSON of harness.agent.v1.SessionInfo.
 */
export const relaySession = pgTable(
  "relay_session",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    hostId: text("host_id").notNull(),
    deviceId: text("device_id").notNull(),
    epoch: int8("epoch").notNull(),
    ackedSeq: int8("acked_seq").notNull().default(0),
    info: jsonb("info").notNull(),
    status: integer("status").notNull().default(0),
    /** False after UnregisterSession: history stays readable, commands are refused. */
    active: boolean("active").notNull().default(true),
    createdAt: ts("created_at").notNull(),
    updatedAt: ts("updated_at").notNull(),
  },
  (t) => [index("relay_session_host_idx").on(t.hostId), index("relay_session_user_idx").on(t.userId)],
);

/** Durable live events (harness.agent.v1.SessionEvent as base64 binary protobuf) by session-local seq. */
export const relayEvent = pgTable(
  "relay_event",
  {
    sessionId: text("session_id")
      .notNull()
      .references(() => relaySession.id, { onDelete: "cascade" }),
    seq: int8("seq").notNull(),
    uuid: text("uuid").notNull(),
    epoch: int8("epoch").notNull(),
    event: text("event").notNull(),
    createdAt: ts("created_at").notNull(),
  },
  (t) => [primaryKey({ columns: [t.sessionId, t.seq] }), uniqueIndex("relay_event_uuid_idx").on(t.sessionId, t.uuid)],
);

/**
 * Downstream commands (harness.relay.v1.Command as base64 binary protobuf), delivered in `ord` order until
 * acked or expired. `session_id` is "" for host-level commands (spawn); `ack` is the base64 CommandAck.
 */
export const relayCommand = pgTable(
  "relay_command",
  {
    id: text("id").primaryKey(),
    ord: bigserial("ord", { mode: "number" }).notNull(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    /** Target host for host-level commands; "" for session commands (routed to the session owner). */
    hostId: text("host_id").notNull().default(""),
    sessionId: text("session_id").notNull().default(""),
    kind: text("kind").notNull(),
    command: text("command").notNull(),
    /** CommandOptions.idempotency_key; null when not given (nulls never collide). */
    idempotencyKey: text("idempotency_key"),
    /** queued | acked */
    state: text("state").notNull().default("queued"),
    ack: text("ack"),
    createdAt: ts("created_at").notNull(),
    expiresAt: ts("expires_at").notNull(),
    ackedAt: ts("acked_at"),
  },
  (t) => [
    uniqueIndex("relay_command_idem_idx").on(t.userId, t.idempotencyKey),
    index("relay_command_session_idx").on(t.sessionId, t.state),
    index("relay_command_host_idx").on(t.hostId, t.state),
  ],
);
