CREATE TABLE "chat_app" (
	"provider" integer PRIMARY KEY NOT NULL,
	"enabled" boolean DEFAULT true NOT NULL,
	"match_by_email" boolean DEFAULT true NOT NULL,
	"team_id" text DEFAULT '' NOT NULL,
	"team_name" text DEFAULT '' NOT NULL,
	"bot_user_id" text DEFAULT '' NOT NULL,
	"secrets_enc" text DEFAULT '' NOT NULL,
	"last_event_at" timestamp with time zone,
	"last_error" text DEFAULT '' NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"updated_by" text DEFAULT '' NOT NULL
);
--> statement-breakpoint
CREATE TABLE "chat_delivery" (
	"key" text PRIMARY KEY NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "chat_identity" (
	"provider" integer NOT NULL,
	"team_id" text NOT NULL,
	"external_user_id" text NOT NULL,
	"user_id" text NOT NULL,
	"display_name" text DEFAULT '' NOT NULL,
	"linked_via" text NOT NULL,
	"linked_at" timestamp with time zone NOT NULL,
	CONSTRAINT "chat_identity_provider_team_id_external_user_id_pk" PRIMARY KEY("provider","team_id","external_user_id")
);
--> statement-breakpoint
CREATE TABLE "chat_link_code" (
	"code_hash" text PRIMARY KEY NOT NULL,
	"provider" integer NOT NULL,
	"user_id" text NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "chat_thread" (
	"id" text PRIMARY KEY NOT NULL,
	"provider" integer NOT NULL,
	"team_id" text NOT NULL,
	"channel" text NOT NULL,
	"thread_id" text NOT NULL,
	"user_id" text NOT NULL,
	"session_id" text DEFAULT '' NOT NULL,
	"host_id" text DEFAULT '' NOT NULL,
	"status" text NOT NULL,
	"last_seq" bigint DEFAULT 0 NOT NULL,
	"progress_id" text DEFAULT '' NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "relay_host" ADD COLUMN "remote_starts" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "chat_identity" ADD CONSTRAINT "chat_identity_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "chat_link_code" ADD CONSTRAINT "chat_link_code_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "chat_thread" ADD CONSTRAINT "chat_thread_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "chat_identity_user_idx" ON "chat_identity" USING btree ("user_id");--> statement-breakpoint
CREATE UNIQUE INDEX "chat_thread_ref_idx" ON "chat_thread" USING btree ("provider","team_id","channel","thread_id");--> statement-breakpoint
CREATE INDEX "chat_thread_session_idx" ON "chat_thread" USING btree ("session_id");