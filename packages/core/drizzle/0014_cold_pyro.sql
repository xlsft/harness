CREATE TABLE "artifact" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_id" text NOT NULL,
	"title" text NOT NULL,
	"visibility" text DEFAULT 'private' NOT NULL,
	"session_id" text DEFAULT '' NOT NULL,
	"current_version" integer DEFAULT 1 NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "artifact_version" (
	"artifact_id" text NOT NULL,
	"number" integer NOT NULL,
	"sha256" text NOT NULL,
	"name" text DEFAULT '' NOT NULL,
	"content_type" text NOT NULL,
	"size" bigint NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "artifact_version_artifact_id_number_pk" PRIMARY KEY("artifact_id","number")
);
--> statement-breakpoint
CREATE TABLE "knowledge_chunk" (
	"document_id" text NOT NULL,
	"index" integer NOT NULL,
	"heading" text NOT NULL,
	"text" text NOT NULL,
	"search" "tsvector" GENERATED ALWAYS AS (setweight(to_tsvector('english', heading), 'A') || setweight(to_tsvector('simple', heading), 'A') || to_tsvector('english', text) || to_tsvector('simple', text)) STORED NOT NULL,
	CONSTRAINT "knowledge_chunk_document_id_index_pk" PRIMARY KEY("document_id","index")
);
--> statement-breakpoint
CREATE TABLE "knowledge_document" (
	"id" text PRIMARY KEY NOT NULL,
	"scope" text NOT NULL,
	"owner_id" text NOT NULL,
	"title" text NOT NULL,
	"source_name" text DEFAULT '' NOT NULL,
	"content" text NOT NULL,
	"size" bigint NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "tracker_task" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_id" text NOT NULL,
	"title" text NOT NULL,
	"description" text DEFAULT '' NOT NULL,
	"status" text DEFAULT 'todo' NOT NULL,
	"priority" text DEFAULT 'normal' NOT NULL,
	"project" text DEFAULT '' NOT NULL,
	"session_ids" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"blocked_by" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"closed_at" timestamp with time zone
);
--> statement-breakpoint
ALTER TABLE "artifact" ADD CONSTRAINT "artifact_owner_id_user_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "artifact_version" ADD CONSTRAINT "artifact_version_artifact_id_artifact_id_fk" FOREIGN KEY ("artifact_id") REFERENCES "public"."artifact"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "knowledge_chunk" ADD CONSTRAINT "knowledge_chunk_document_id_knowledge_document_id_fk" FOREIGN KEY ("document_id") REFERENCES "public"."knowledge_document"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "knowledge_document" ADD CONSTRAINT "knowledge_document_owner_id_user_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "tracker_task" ADD CONSTRAINT "tracker_task_owner_id_user_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "artifact_owner_idx" ON "artifact" USING btree ("owner_id","updated_at");--> statement-breakpoint
CREATE INDEX "artifact_visibility_idx" ON "artifact" USING btree ("visibility","updated_at");--> statement-breakpoint
CREATE INDEX "artifact_version_sha_idx" ON "artifact_version" USING btree ("sha256");--> statement-breakpoint
CREATE INDEX "knowledge_chunk_search_idx" ON "knowledge_chunk" USING gin ("search");--> statement-breakpoint
CREATE INDEX "knowledge_document_scope_idx" ON "knowledge_document" USING btree ("scope","owner_id");--> statement-breakpoint
CREATE INDEX "tracker_task_owner_idx" ON "tracker_task" USING btree ("owner_id","status","updated_at");--> statement-breakpoint
CREATE INDEX "sync_session_entry_search_idx" ON "sync_session_entry" USING gin (to_tsvector('simple', "search_text"));