CREATE TABLE "cloud_session" (
	"id" text PRIMARY KEY NOT NULL,
	"host_id" text NOT NULL,
	"user_id" text NOT NULL,
	"work_id" text NOT NULL,
	"device_id" text DEFAULT '' NOT NULL,
	"session_id" text DEFAULT '' NOT NULL,
	"state" integer NOT NULL,
	"container_id" text DEFAULT '' NOT NULL,
	"image" text DEFAULT '' NOT NULL,
	"repository_key" text DEFAULT '' NOT NULL,
	"error" text DEFAULT '' NOT NULL,
	"disk_bytes" bigint DEFAULT 0 NOT NULL,
	"starts" integer DEFAULT 0 NOT NULL,
	"attempts" integer DEFAULT 0 NOT NULL,
	"claimed" boolean DEFAULT false NOT NULL,
	"working_sessions" integer DEFAULT 0 NOT NULL,
	"starting_since" timestamp with time zone,
	"started_at" timestamp with time zone,
	"registered_at" timestamp with time zone,
	"reported_at" timestamp with time zone,
	"last_active_at" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "agent_host" ADD COLUMN "cloud_config" jsonb DEFAULT '{}'::jsonb NOT NULL;--> statement-breakpoint
ALTER TABLE "cloud_session" ADD CONSTRAINT "cloud_session_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "cloud_session_host_idx" ON "cloud_session" USING btree ("host_id","state");--> statement-breakpoint
CREATE INDEX "cloud_session_user_idx" ON "cloud_session" USING btree ("user_id","state");--> statement-breakpoint
CREATE UNIQUE INDEX "cloud_session_work_idx" ON "cloud_session" USING btree ("work_id");--> statement-breakpoint
CREATE INDEX "cloud_session_device_idx" ON "cloud_session" USING btree ("device_id");