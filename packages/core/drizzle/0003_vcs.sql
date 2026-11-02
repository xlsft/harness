CREATE TABLE "vcs_autofix_job" (
	"id" text PRIMARY KEY NOT NULL,
	"session_id" text NOT NULL,
	"user_id" text NOT NULL,
	"event_seq" bigint NOT NULL,
	"kind" integer NOT NULL,
	"repository_key" text NOT NULL,
	"pr_number" bigint DEFAULT 0 NOT NULL,
	"head_sha" text DEFAULT '' NOT NULL,
	"status" text DEFAULT 'pending' NOT NULL,
	"payload" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "vcs_connection" (
	"id" text PRIMARY KEY NOT NULL,
	"type" integer NOT NULL,
	"display_name" text DEFAULT '' NOT NULL,
	"base_url" text NOT NULL,
	"app_id" text DEFAULT '' NOT NULL,
	"app_slug" text DEFAULT '' NOT NULL,
	"client_id" text DEFAULT '' NOT NULL,
	"secrets_enc" text DEFAULT '' NOT NULL,
	"allow_private_network" boolean DEFAULT false NOT NULL,
	"healthy" boolean DEFAULT false NOT NULL,
	"last_error" text DEFAULT '' NOT NULL,
	"last_checked_at" timestamp with time zone,
	"last_webhook_at" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"updated_by" text DEFAULT '' NOT NULL
);
--> statement-breakpoint
CREATE TABLE "vcs_delivery" (
	"key" text PRIMARY KEY NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "vcs_device_provider" (
	"user_id" text NOT NULL,
	"device_id" text DEFAULT '' NOT NULL,
	"type" integer NOT NULL,
	"version" text DEFAULT '' NOT NULL,
	"hosts" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"reported_at" timestamp with time zone NOT NULL,
	CONSTRAINT "vcs_device_provider_user_id_device_id_type_pk" PRIMARY KEY("user_id","device_id","type")
);
--> statement-breakpoint
CREATE TABLE "vcs_event" (
	"seq" bigserial PRIMARY KEY NOT NULL,
	"kind" integer NOT NULL,
	"source" integer NOT NULL,
	"connection_id" text DEFAULT '' NOT NULL,
	"delivery_id" text DEFAULT '' NOT NULL,
	"repository_key" text NOT NULL,
	"pr_number" bigint DEFAULT 0 NOT NULL,
	"head_branch" text DEFAULT '' NOT NULL,
	"pull_request" jsonb,
	"sessions" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"actor_user_id" text DEFAULT '' NOT NULL,
	"detail" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"occurred_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "vcs_installation" (
	"connection_id" text NOT NULL,
	"installation_id" bigint NOT NULL,
	"account_login" text NOT NULL,
	"account_type" text DEFAULT '' NOT NULL,
	"suspended" boolean DEFAULT false NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "vcs_installation_connection_id_installation_id_pk" PRIMARY KEY("connection_id","installation_id")
);
--> statement-breakpoint
CREATE TABLE "vcs_manifest_flow" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"state_hash" text NOT NULL,
	"base_url" text NOT NULL,
	"organization" text DEFAULT '' NOT NULL,
	"app_name" text DEFAULT '' NOT NULL,
	"allow_private_network" boolean DEFAULT false NOT NULL,
	"status" text DEFAULT 'pending' NOT NULL,
	"connection_id" text DEFAULT '' NOT NULL,
	"error" text DEFAULT '' NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "vcs_manifest_flow_state_hash_unique" UNIQUE("state_hash")
);
--> statement-breakpoint
CREATE TABLE "vcs_oauth_flow" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"connection_id" text NOT NULL,
	"state_hash" text NOT NULL,
	"data_enc" text NOT NULL,
	"status" text DEFAULT 'pending' NOT NULL,
	"error" text DEFAULT '' NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"completed_at" timestamp with time zone,
	CONSTRAINT "vcs_oauth_flow_state_hash_unique" UNIQUE("state_hash")
);
--> statement-breakpoint
CREATE TABLE "vcs_pull_request" (
	"repository_key" text NOT NULL,
	"number" bigint NOT NULL,
	"connection_id" text DEFAULT '' NOT NULL,
	"source" integer NOT NULL,
	"head_branch" text DEFAULT '' NOT NULL,
	"head_sha" text DEFAULT '' NOT NULL,
	"base_branch" text DEFAULT '' NOT NULL,
	"data" jsonb NOT NULL,
	"checks" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"reported_by" text DEFAULT '' NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "vcs_pull_request_repository_key_number_pk" PRIMARY KEY("repository_key","number")
);
--> statement-breakpoint
CREATE TABLE "vcs_session_link" (
	"session_id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"repository_key" text DEFAULT '' NOT NULL,
	"branch" text DEFAULT '' NOT NULL,
	"pr_number" bigint DEFAULT 0 NOT NULL,
	"auto_fix_ci" boolean DEFAULT false NOT NULL,
	"auto_fix_reviews" boolean DEFAULT false NOT NULL,
	"auto_fix_conflicts" boolean DEFAULT false NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "vcs_user_token" (
	"user_id" text NOT NULL,
	"connection_id" text NOT NULL,
	"state" text NOT NULL,
	"error" text DEFAULT '' NOT NULL,
	"tokens_enc" text,
	"login" text DEFAULT '' NOT NULL,
	"expires_at" timestamp with time zone,
	"refresh_expires_at" timestamp with time zone,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "vcs_user_token_user_id_connection_id_pk" PRIMARY KEY("user_id","connection_id")
);
--> statement-breakpoint
ALTER TABLE "vcs_autofix_job" ADD CONSTRAINT "vcs_autofix_job_session_id_sync_session_id_fk" FOREIGN KEY ("session_id") REFERENCES "public"."sync_session"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_device_provider" ADD CONSTRAINT "vcs_device_provider_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_installation" ADD CONSTRAINT "vcs_installation_connection_id_vcs_connection_id_fk" FOREIGN KEY ("connection_id") REFERENCES "public"."vcs_connection"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_manifest_flow" ADD CONSTRAINT "vcs_manifest_flow_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_oauth_flow" ADD CONSTRAINT "vcs_oauth_flow_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_oauth_flow" ADD CONSTRAINT "vcs_oauth_flow_connection_id_vcs_connection_id_fk" FOREIGN KEY ("connection_id") REFERENCES "public"."vcs_connection"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_session_link" ADD CONSTRAINT "vcs_session_link_session_id_sync_session_id_fk" FOREIGN KEY ("session_id") REFERENCES "public"."sync_session"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_user_token" ADD CONSTRAINT "vcs_user_token_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "vcs_user_token" ADD CONSTRAINT "vcs_user_token_connection_id_vcs_connection_id_fk" FOREIGN KEY ("connection_id") REFERENCES "public"."vcs_connection"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "vcs_autofix_job_session_idx" ON "vcs_autofix_job" USING btree ("session_id","created_at");--> statement-breakpoint
CREATE INDEX "vcs_event_repo_idx" ON "vcs_event" USING btree ("repository_key","seq");--> statement-breakpoint
CREATE INDEX "vcs_event_created_idx" ON "vcs_event" USING btree ("created_at");--> statement-breakpoint
CREATE INDEX "vcs_installation_account_idx" ON "vcs_installation" USING btree ("connection_id","account_login");--> statement-breakpoint
CREATE INDEX "vcs_oauth_flow_user_idx" ON "vcs_oauth_flow" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "vcs_pull_request_branch_idx" ON "vcs_pull_request" USING btree ("repository_key","head_branch");