CREATE TABLE "agent_host" (
	"id" text PRIMARY KEY NOT NULL,
	"name" text NOT NULL,
	"scope" text NOT NULL,
	"owner_user_id" text NOT NULL,
	"group_id" text DEFAULT '' NOT NULL,
	"draining" boolean DEFAULT false NOT NULL,
	"max_sessions" integer DEFAULT 4 NOT NULL,
	"max_sessions_per_user" integer DEFAULT 0 NOT NULL,
	"allowed_repositories" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"isolation" text DEFAULT '' NOT NULL,
	"sandbox_driver" text DEFAULT '' NOT NULL,
	"os" text DEFAULT '' NOT NULL,
	"arch" text DEFAULT '' NOT NULL,
	"client_version" text DEFAULT '' NOT NULL,
	"cpu_count" integer DEFAULT 0 NOT NULL,
	"load_average" double precision DEFAULT 0 NOT NULL,
	"disk_free_bytes" bigint DEFAULT 0 NOT NULL,
	"last_seen_at" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "agent_host_away" (
	"user_id" text PRIMARY KEY NOT NULL,
	"host_id" text DEFAULT '' NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "agent_host_device" (
	"device_id" text PRIMARY KEY NOT NULL,
	"host_id" text NOT NULL,
	"user_id" text NOT NULL,
	"role" text NOT NULL,
	"working_sessions" integer DEFAULT 0 NOT NULL,
	"reported_at" timestamp with time zone
);
--> statement-breakpoint
CREATE TABLE "host_work" (
	"id" text PRIMARY KEY NOT NULL,
	"host_id" text NOT NULL,
	"user_id" text NOT NULL,
	"kind" integer NOT NULL,
	"state" integer NOT NULL,
	"repository_key" text DEFAULT '' NOT NULL,
	"base_ref" text DEFAULT '' NOT NULL,
	"branch" text DEFAULT '' NOT NULL,
	"landing" integer DEFAULT 0 NOT NULL,
	"fresh_clone" boolean DEFAULT false NOT NULL,
	"prompt" text DEFAULT '' NOT NULL,
	"title" text DEFAULT '' NOT NULL,
	"model" text DEFAULT '' NOT NULL,
	"effort" integer DEFAULT 0 NOT NULL,
	"permission_mode" integer DEFAULT 0 NOT NULL,
	"job" text DEFAULT '' NOT NULL,
	"fork_session_id" text DEFAULT '' NOT NULL,
	"session_id" text DEFAULT '' NOT NULL,
	"pull_request_url" text DEFAULT '' NOT NULL,
	"error" text DEFAULT '' NOT NULL,
	"attempts" integer DEFAULT 0 NOT NULL,
	"device_id" text DEFAULT '' NOT NULL,
	"lease_until" timestamp with time zone,
	"idempotency_key" text,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "schedule_run_claim" (
	"user_id" text NOT NULL,
	"schedule_id" text NOT NULL,
	"due_at" text NOT NULL,
	"device_id" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "schedule_run_claim_user_id_schedule_id_due_at_pk" PRIMARY KEY("user_id","schedule_id","due_at")
);
--> statement-breakpoint
ALTER TABLE "device" ADD COLUMN "kind" text DEFAULT '' NOT NULL;--> statement-breakpoint
ALTER TABLE "agent_host" ADD CONSTRAINT "agent_host_owner_user_id_user_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "agent_host_away" ADD CONSTRAINT "agent_host_away_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "agent_host_device" ADD CONSTRAINT "agent_host_device_device_id_device_id_fk" FOREIGN KEY ("device_id") REFERENCES "public"."device"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "agent_host_device" ADD CONSTRAINT "agent_host_device_host_id_agent_host_id_fk" FOREIGN KEY ("host_id") REFERENCES "public"."agent_host"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "agent_host_device" ADD CONSTRAINT "agent_host_device_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "host_work" ADD CONSTRAINT "host_work_host_id_agent_host_id_fk" FOREIGN KEY ("host_id") REFERENCES "public"."agent_host"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "host_work" ADD CONSTRAINT "host_work_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "schedule_run_claim" ADD CONSTRAINT "schedule_run_claim_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "agent_host_owner_idx" ON "agent_host" USING btree ("owner_user_id");--> statement-breakpoint
CREATE INDEX "agent_host_group_idx" ON "agent_host" USING btree ("group_id");--> statement-breakpoint
CREATE INDEX "agent_host_device_host_idx" ON "agent_host_device" USING btree ("host_id");--> statement-breakpoint
CREATE UNIQUE INDEX "agent_host_device_role_idx" ON "agent_host_device" USING btree ("host_id","user_id","role");--> statement-breakpoint
CREATE INDEX "host_work_host_idx" ON "host_work" USING btree ("host_id","state");--> statement-breakpoint
CREATE INDEX "host_work_user_idx" ON "host_work" USING btree ("user_id","created_at");--> statement-breakpoint
CREATE UNIQUE INDEX "host_work_idem_idx" ON "host_work" USING btree ("user_id","idempotency_key");