CREATE TABLE "knowledge_connection" (
	"id" text PRIMARY KEY NOT NULL,
	"provider" text NOT NULL,
	"name" text NOT NULL,
	"base_url" text DEFAULT '' NOT NULL,
	"auth" text DEFAULT '' NOT NULL,
	"login" text DEFAULT '' NOT NULL,
	"oauth_client_id" text DEFAULT '' NOT NULL,
	"secrets_enc" text DEFAULT '' NOT NULL,
	"enabled" boolean DEFAULT true NOT NULL,
	"allow_private_network" boolean DEFAULT false NOT NULL,
	"query" text DEFAULT '' NOT NULL,
	"options" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"borrow_connection_id" text DEFAULT '' NOT NULL,
	"migrated_from" text DEFAULT '' NOT NULL,
	"healthy" boolean DEFAULT false NOT NULL,
	"last_error" text DEFAULT '' NOT NULL,
	"last_checked_at" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"updated_by" text DEFAULT '' NOT NULL
);
--> statement-breakpoint
ALTER TABLE "tracker_connection" ADD COLUMN "options" jsonb DEFAULT '{}'::jsonb NOT NULL;--> statement-breakpoint
-- Data: a company tracker connection that served Knowledge itself becomes a knowledge source connection borrowing its sign-in, keeping its query; migrated_from keeps old conn:<tracker id>:<article> ids readable. docs/design/tracker.md, section 6.
INSERT INTO "knowledge_connection" ("id", "provider", "name", "enabled", "query", "borrow_connection_id", "migrated_from", "created_at", "updated_at", "updated_by") SELECT 'kc_' || substr(md5(random()::text || "id"), 1, 16), 'youtrack-kb', 'YouTrack Knowledge Base · ' || "name", "enabled", "knowledge_query", "id", "id", now(), now(), "updated_by" FROM "tracker_connection" WHERE "knowledge_enabled" AND "provider" = 'youtrack';
--> statement-breakpoint
UPDATE "tracker_connection" SET "knowledge_enabled" = false, "knowledge_query" = '' WHERE "knowledge_enabled" AND "provider" = 'youtrack';
