import { bigint, boolean, doublePrecision, index, integer, jsonb, pgTable, primaryKey, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";

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

export const provider = pgTable("provider", {
  id: text("id").primaryKey(),
  /** anthropic | openai-chat */
  type: text("type").notNull(),
  displayName: text("display_name"),
  baseUrl: text("base_url").notNull(),
  /** AES-256-GCM, see lib/crypto.ts. Null when the provider needs no key (e.g. local Ollama). */
  apiKeyEnc: text("api_key_enc"),
  apiKeyHint: text("api_key_hint"),
  /** Encrypted JSON object of extra headers. */
  headersEnc: text("headers_enc"),
  allowPrivateNetwork: boolean("allow_private_network").notNull().default(false),
  enabled: boolean("enabled").notNull().default(true),
  createdAt: ts("created_at").notNull(),
  updatedAt: ts("updated_at").notNull(),
});

export const model = pgTable("model", {
  /** Alias the client sends in `model`. */
  id: text("id").primaryKey(),
  displayName: text("display_name").notNull(),
  providerId: text("provider_id")
    .notNull()
    .references(() => provider.id, { onDelete: "restrict" }),
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
