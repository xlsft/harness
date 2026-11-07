CREATE TABLE "attempt" (
	"group_id" text NOT NULL,
	"index" integer NOT NULL,
	"state" integer NOT NULL,
	"work_id" text DEFAULT '' NOT NULL,
	"session_id" text DEFAULT '' NOT NULL,
	"branch" text DEFAULT '' NOT NULL,
	"error" text DEFAULT '' NOT NULL,
	"check" text DEFAULT '' NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "attempt_group_id_index_pk" PRIMARY KEY("group_id","index")
);
--> statement-breakpoint
CREATE TABLE "attempt_group" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"host_id" text DEFAULT '' NOT NULL,
	"device_id" text DEFAULT '' NOT NULL,
	"cwd" text DEFAULT '' NOT NULL,
	"prompt" text NOT NULL,
	"title" text DEFAULT '' NOT NULL,
	"repository_key" text DEFAULT '' NOT NULL,
	"base_ref" text DEFAULT '' NOT NULL,
	"model" text DEFAULT '' NOT NULL,
	"effort" integer DEFAULT 0 NOT NULL,
	"permission_mode" integer DEFAULT 0 NOT NULL,
	"test_command" text DEFAULT '' NOT NULL,
	"parallel" integer NOT NULL,
	"budget_usd" double precision DEFAULT 0 NOT NULL,
	"kept_index" integer DEFAULT -1 NOT NULL,
	"idempotency_key" text,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "host_work" ADD COLUMN "attempt_group_id" text DEFAULT '' NOT NULL;--> statement-breakpoint
ALTER TABLE "host_work" ADD COLUMN "attempt_index" integer DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "host_work" ADD COLUMN "test_command" text DEFAULT '' NOT NULL;--> statement-breakpoint
ALTER TABLE "host_work" ADD COLUMN "budget_usd" double precision DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "host_work" ADD COLUMN "discard_requested" boolean DEFAULT false NOT NULL;--> statement-breakpoint
ALTER TABLE "attempt" ADD CONSTRAINT "attempt_group_id_attempt_group_id_fk" FOREIGN KEY ("group_id") REFERENCES "public"."attempt_group"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "attempt_group" ADD CONSTRAINT "attempt_group_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "attempt_group_user_idx" ON "attempt_group" USING btree ("user_id","created_at");--> statement-breakpoint
CREATE UNIQUE INDEX "attempt_group_idem_idx" ON "attempt_group" USING btree ("user_id","idempotency_key");--> statement-breakpoint
CREATE INDEX "host_work_group_idx" ON "host_work" USING btree ("attempt_group_id");