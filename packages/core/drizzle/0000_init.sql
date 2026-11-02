CREATE TABLE "access_token" (
	"token_hash" text PRIMARY KEY NOT NULL,
	"device_id" text NOT NULL,
	"user_id" text NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "account" (
	"id" text PRIMARY KEY NOT NULL,
	"account_id" text NOT NULL,
	"provider_id" text NOT NULL,
	"user_id" text NOT NULL,
	"access_token" text,
	"refresh_token" text,
	"id_token" text,
	"access_token_expires_at" timestamp with time zone,
	"refresh_token_expires_at" timestamp with time zone,
	"scope" text,
	"password" text,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "device" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"name" text NOT NULL,
	"client_name" text,
	"os" text,
	"arch" text,
	"user_agent" text,
	"token_hash" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"last_seen_at" timestamp with time zone,
	"revoked_at" timestamp with time zone,
	CONSTRAINT "device_token_hash_unique" UNIQUE("token_hash")
);
--> statement-breakpoint
CREATE TABLE "device_code" (
	"device_code_hash" text PRIMARY KEY NOT NULL,
	"user_code" text NOT NULL,
	"client_name" text,
	"device_name" text,
	"os" text,
	"arch" text,
	"status" text DEFAULT 'pending' NOT NULL,
	"user_id" text,
	"interval_sec" integer NOT NULL,
	"last_polled_at" timestamp with time zone,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "device_code_user_code_unique" UNIQUE("user_code")
);
--> statement-breakpoint
CREATE TABLE "model" (
	"id" text PRIMARY KEY NOT NULL,
	"display_name" text NOT NULL,
	"provider_id" text NOT NULL,
	"upstream_model" text NOT NULL,
	"context_window" integer NOT NULL,
	"max_output_tokens" integer NOT NULL,
	"capabilities" jsonb NOT NULL,
	"pricing" jsonb,
	"fallbacks" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"extra_body" jsonb,
	"roles" jsonb,
	"enabled" boolean DEFAULT true NOT NULL,
	"sort_order" integer DEFAULT 0 NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "model_negotiation" (
	"model_id" text NOT NULL,
	"feature" text NOT NULL,
	"reason" text,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "model_negotiation_model_id_feature_pk" PRIMARY KEY("model_id","feature")
);
--> statement-breakpoint
CREATE TABLE "provider" (
	"id" text PRIMARY KEY NOT NULL,
	"type" text NOT NULL,
	"display_name" text,
	"base_url" text NOT NULL,
	"api_key_enc" text,
	"api_key_hint" text,
	"headers_enc" text,
	"allow_private_network" boolean DEFAULT false NOT NULL,
	"enabled" boolean DEFAULT true NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "session" (
	"id" text PRIMARY KEY NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"token" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"ip_address" text,
	"user_agent" text,
	"user_id" text NOT NULL,
	"impersonated_by" text,
	CONSTRAINT "session_token_unique" UNIQUE("token")
);
--> statement-breakpoint
CREATE TABLE "setting" (
	"key" text PRIMARY KEY NOT NULL,
	"value" jsonb NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "sync_counter" (
	"name" text PRIMARY KEY NOT NULL,
	"value" bigint NOT NULL
);
--> statement-breakpoint
CREATE TABLE "sync_document" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_id" text NOT NULL,
	"scope" integer NOT NULL,
	"project_key" text DEFAULT '' NOT NULL,
	"kind" integer NOT NULL,
	"key" text NOT NULL,
	"version" bigint NOT NULL,
	"content" text DEFAULT '' NOT NULL,
	"files" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"content_hash" text NOT NULL,
	"source" text DEFAULT '' NOT NULL,
	"target_roles" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"updated_by" text NOT NULL,
	"deleted_at" timestamp with time zone
);
--> statement-breakpoint
CREATE TABLE "sync_document_revision" (
	"document_id" text NOT NULL,
	"version" bigint NOT NULL,
	"content" text NOT NULL,
	"files" jsonb NOT NULL,
	"content_hash" text NOT NULL,
	"deleted" boolean DEFAULT false NOT NULL,
	"updated_by" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "sync_document_revision_document_id_version_pk" PRIMARY KEY("document_id","version")
);
--> statement-breakpoint
CREATE TABLE "sync_session" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"info" jsonb NOT NULL,
	"title" text DEFAULT '' NOT NULL,
	"cwd" text DEFAULT '' NOT NULL,
	"repo" text DEFAULT '' NOT NULL,
	"device_id" text DEFAULT '' NOT NULL,
	"status" integer DEFAULT 0 NOT NULL,
	"archived" boolean DEFAULT false NOT NULL,
	"prompt_bundle_version" text DEFAULT '' NOT NULL,
	"last_seq" bigint DEFAULT 0 NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"last_activity_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "sync_session_entry" (
	"session_id" text NOT NULL,
	"seq" bigint NOT NULL,
	"uuid" text NOT NULL,
	"agent_id" text DEFAULT '' NOT NULL,
	"parent_uuid" text DEFAULT '' NOT NULL,
	"type" text DEFAULT '' NOT NULL,
	"timestamp" text DEFAULT '' NOT NULL,
	"entry_json" text NOT NULL,
	"search_text" text DEFAULT '' NOT NULL,
	CONSTRAINT "sync_session_entry_session_id_seq_pk" PRIMARY KEY("session_id","seq")
);
--> statement-breakpoint
CREATE TABLE "usage_record" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"device_id" text,
	"model_id" text NOT NULL,
	"provider_id" text,
	"upstream_model" text,
	"request_class" text,
	"session_id" text,
	"agent_id" text,
	"input_tokens" integer DEFAULT 0 NOT NULL,
	"output_tokens" integer DEFAULT 0 NOT NULL,
	"cache_read_tokens" integer DEFAULT 0 NOT NULL,
	"cache_write_tokens" integer DEFAULT 0 NOT NULL,
	"cost_usd" double precision DEFAULT 0 NOT NULL,
	"retries" integer DEFAULT 0 NOT NULL,
	"status" text NOT NULL,
	"stop_reason" text,
	"error_type" text,
	"duration_ms" integer,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "user" (
	"id" text PRIMARY KEY NOT NULL,
	"name" text NOT NULL,
	"email" text NOT NULL,
	"email_verified" boolean DEFAULT false NOT NULL,
	"image" text,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"role" text,
	"banned" boolean DEFAULT false,
	"ban_reason" text,
	"ban_expires" timestamp with time zone,
	CONSTRAINT "user_email_unique" UNIQUE("email")
);
--> statement-breakpoint
CREATE TABLE "verification" (
	"id" text PRIMARY KEY NOT NULL,
	"identifier" text NOT NULL,
	"value" text NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "access_token" ADD CONSTRAINT "access_token_device_id_device_id_fk" FOREIGN KEY ("device_id") REFERENCES "public"."device"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "access_token" ADD CONSTRAINT "access_token_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "account" ADD CONSTRAINT "account_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "device" ADD CONSTRAINT "device_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "device_code" ADD CONSTRAINT "device_code_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "model" ADD CONSTRAINT "model_provider_id_provider_id_fk" FOREIGN KEY ("provider_id") REFERENCES "public"."provider"("id") ON DELETE restrict ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "model_negotiation" ADD CONSTRAINT "model_negotiation_model_id_model_id_fk" FOREIGN KEY ("model_id") REFERENCES "public"."model"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "session" ADD CONSTRAINT "session_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "sync_session" ADD CONSTRAINT "sync_session_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "sync_session_entry" ADD CONSTRAINT "sync_session_entry_session_id_sync_session_id_fk" FOREIGN KEY ("session_id") REFERENCES "public"."sync_session"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "usage_record" ADD CONSTRAINT "usage_record_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "access_token_device_idx" ON "access_token" USING btree ("device_id");--> statement-breakpoint
CREATE INDEX "account_user_idx" ON "account" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "device_user_idx" ON "device" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "session_user_idx" ON "session" USING btree ("user_id");--> statement-breakpoint
CREATE UNIQUE INDEX "sync_document_key_idx" ON "sync_document" USING btree ("owner_id","scope","project_key","kind","key");--> statement-breakpoint
CREATE INDEX "sync_document_version_idx" ON "sync_document" USING btree ("version");--> statement-breakpoint
CREATE INDEX "sync_session_user_activity_idx" ON "sync_session" USING btree ("user_id","last_activity_at");--> statement-breakpoint
CREATE UNIQUE INDEX "sync_session_entry_uuid_idx" ON "sync_session_entry" USING btree ("session_id","agent_id","uuid");--> statement-breakpoint
CREATE INDEX "usage_user_created_idx" ON "usage_record" USING btree ("user_id","created_at");--> statement-breakpoint
CREATE INDEX "usage_session_idx" ON "usage_record" USING btree ("session_id");