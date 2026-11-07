CREATE TABLE "relay_inbox" (
	"session_id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"host_id" text DEFAULT '' NOT NULL,
	"grp" integer DEFAULT 0 NOT NULL,
	"state" jsonb NOT NULL,
	"scratch" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"project_run" boolean DEFAULT false NOT NULL,
	"active" boolean DEFAULT true NOT NULL,
	"attention_at" timestamp with time zone,
	"read_at" timestamp with time zone,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "relay_inbox" ADD CONSTRAINT "relay_inbox_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "relay_inbox_user_idx" ON "relay_inbox" USING btree ("user_id","grp","attention_at");