CREATE TABLE "object_upload" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_id" text NOT NULL,
	"sha256" text NOT NULL,
	"size" bigint NOT NULL,
	"store_upload_id" text NOT NULL,
	"parts" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "stored_object" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_id" text NOT NULL,
	"session_id" text DEFAULT '' NOT NULL,
	"kind" text NOT NULL,
	"name" text DEFAULT '' NOT NULL,
	"sha256" text NOT NULL,
	"size" bigint NOT NULL,
	"content_type" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "stored_object" ADD CONSTRAINT "stored_object_owner_id_user_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "object_upload_owner_sha_idx" ON "object_upload" USING btree ("owner_id","sha256");--> statement-breakpoint
CREATE INDEX "object_upload_updated_idx" ON "object_upload" USING btree ("updated_at");--> statement-breakpoint
CREATE UNIQUE INDEX "stored_object_use_idx" ON "stored_object" USING btree ("owner_id","session_id","kind","sha256","name");--> statement-breakpoint
CREATE INDEX "stored_object_owner_sha_idx" ON "stored_object" USING btree ("owner_id","sha256");--> statement-breakpoint
CREATE INDEX "stored_object_session_idx" ON "stored_object" USING btree ("owner_id","session_id");