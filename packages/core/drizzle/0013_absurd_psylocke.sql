CREATE TABLE "audit_event" (
	"id" bigserial PRIMARY KEY NOT NULL,
	"at" timestamp with time zone NOT NULL,
	"action" text NOT NULL,
	"category" text NOT NULL,
	"outcome" text NOT NULL,
	"actor_id" text DEFAULT '' NOT NULL,
	"actor_label" text DEFAULT '' NOT NULL,
	"actor_ip" text DEFAULT '' NOT NULL,
	"target_type" text DEFAULT '' NOT NULL,
	"target_id" text DEFAULT '' NOT NULL,
	"target_label" text DEFAULT '' NOT NULL,
	"details" jsonb DEFAULT '{}'::jsonb NOT NULL
);
--> statement-breakpoint
CREATE TABLE "directory_group" (
	"id" text PRIMARY KEY NOT NULL,
	"name" text NOT NULL,
	"source" text NOT NULL,
	"external_id" text DEFAULT '' NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "directory_group_member" (
	"group_id" text NOT NULL,
	"user_id" text NOT NULL,
	CONSTRAINT "directory_group_member_group_id_user_id_pk" PRIMARY KEY("group_id","user_id")
);
--> statement-breakpoint
CREATE TABLE "scim_identity" (
	"user_id" text PRIMARY KEY NOT NULL,
	"user_name" text NOT NULL,
	"external_id" text DEFAULT '' NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "scim_identity_user_name_unique" UNIQUE("user_name")
);
--> statement-breakpoint
CREATE TABLE "scim_token" (
	"id" text PRIMARY KEY NOT NULL,
	"name" text NOT NULL,
	"token_hash" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"last_used_at" timestamp with time zone,
	CONSTRAINT "scim_token_token_hash_unique" UNIQUE("token_hash")
);
--> statement-breakpoint
CREATE TABLE "sso_flow" (
	"id_hash" text PRIMARY KEY NOT NULL,
	"provider_id" text NOT NULL,
	"browser_hash" text NOT NULL,
	"secrets_enc" text,
	"return_to" text DEFAULT '' NOT NULL,
	"test" boolean DEFAULT false NOT NULL,
	"user_id" text,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "sso_provider" (
	"id" text PRIMARY KEY NOT NULL,
	"name" text NOT NULL,
	"protocol" text NOT NULL,
	"enabled" boolean DEFAULT false NOT NULL,
	"config" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"client_secret_enc" text,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "user_mfa" (
	"user_id" text PRIMARY KEY NOT NULL,
	"secret_enc" text NOT NULL,
	"enabled_at" timestamp with time zone,
	"last_step" bigint DEFAULT 0 NOT NULL,
	"recovery_hashes" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "session" ADD COLUMN "mfa_pending" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "directory_group_member" ADD CONSTRAINT "directory_group_member_group_id_directory_group_id_fk" FOREIGN KEY ("group_id") REFERENCES "public"."directory_group"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "directory_group_member" ADD CONSTRAINT "directory_group_member_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "scim_identity" ADD CONSTRAINT "scim_identity_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "user_mfa" ADD CONSTRAINT "user_mfa_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "audit_event_at_idx" ON "audit_event" USING btree ("at");--> statement-breakpoint
CREATE INDEX "audit_event_action_idx" ON "audit_event" USING btree ("category","action");--> statement-breakpoint
CREATE UNIQUE INDEX "directory_group_source_name_idx" ON "directory_group" USING btree ("source","name");--> statement-breakpoint
CREATE INDEX "directory_group_member_user_idx" ON "directory_group_member" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "scim_identity_external_idx" ON "scim_identity" USING btree ("external_id");--> statement-breakpoint
CREATE INDEX "sso_flow_expires_idx" ON "sso_flow" USING btree ("expires_at");