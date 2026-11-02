CREATE TABLE "decision_config" (
	"id" text PRIMARY KEY NOT NULL,
	"config" jsonb NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"updated_by" text
);
--> statement-breakpoint
CREATE TABLE "decision_log" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"device_id" text,
	"session_id" text DEFAULT '' NOT NULL,
	"tool_name" text NOT NULL,
	"input_hash" text NOT NULL,
	"input_preview" text DEFAULT '' NOT NULL,
	"decision" text NOT NULL,
	"provider" text NOT NULL,
	"categories" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"reason" text DEFAULT '' NOT NULL,
	"confidence" double precision,
	"latency_ms" integer NOT NULL,
	"cost_usd" double precision DEFAULT 0 NOT NULL,
	"cached" boolean DEFAULT false NOT NULL,
	"error" text,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "decision_log" ADD CONSTRAINT "decision_log_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "decision_log_session_idx" ON "decision_log" USING btree ("session_id","created_at");--> statement-breakpoint
CREATE INDEX "decision_log_user_idx" ON "decision_log" USING btree ("user_id","created_at");--> statement-breakpoint
CREATE INDEX "decision_log_created_idx" ON "decision_log" USING btree ("created_at");