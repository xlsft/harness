CREATE TABLE "board" (
	"id" text PRIMARY KEY NOT NULL,
	"project_id" text NOT NULL,
	"scope" text NOT NULL,
	"owner_id" text,
	"name" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "board_card" (
	"id" text PRIMARY KEY NOT NULL,
	"board_id" text NOT NULL,
	"stage_id" text NOT NULL,
	"position" double precision NOT NULL,
	"project_id" text NOT NULL,
	"number" integer NOT NULL,
	"priority" text DEFAULT 'normal' NOT NULL,
	"labels" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"assignee_ids" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"due_at" timestamp with time zone,
	"comment_count" integer DEFAULT 0 NOT NULL,
	"title" text NOT NULL,
	"title_edited" boolean DEFAULT false NOT NULL,
	"description" text DEFAULT '' NOT NULL,
	"version" integer DEFAULT 1 NOT NULL,
	"connection_scope" text DEFAULT '' NOT NULL,
	"connection_id" text DEFAULT '' NOT NULL,
	"external_id" text DEFAULT '' NOT NULL,
	"external_url" text DEFAULT '' NOT NULL,
	"external_state" text DEFAULT '' NOT NULL,
	"external_checked_at" timestamp with time zone,
	"session_ids" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"pr_urls" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"branch" text DEFAULT '' NOT NULL,
	"created_by" text DEFAULT '' NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "board_card_activity" (
	"id" text PRIMARY KEY NOT NULL,
	"card_id" text NOT NULL,
	"actor_id" text DEFAULT '' NOT NULL,
	"kind" text NOT NULL,
	"summary" text DEFAULT '' NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "board_card_comment" (
	"id" text PRIMARY KEY NOT NULL,
	"card_id" text NOT NULL,
	"author_id" text NOT NULL,
	"text" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "board_card_output" (
	"id" text PRIMARY KEY NOT NULL,
	"card_id" text NOT NULL,
	"stage_id" text NOT NULL,
	"connection_scope" text NOT NULL,
	"connection_id" text NOT NULL,
	"fields" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"comment" text DEFAULT '' NOT NULL,
	"status" text DEFAULT 'pending' NOT NULL,
	"attempts" integer DEFAULT 0 NOT NULL,
	"last_error" text DEFAULT '' NOT NULL,
	"next_attempt_at" timestamp with time zone NOT NULL,
	"lease_until" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "board_card_relation" (
	"card_id" text NOT NULL,
	"kind" text NOT NULL,
	"other_id" text NOT NULL,
	CONSTRAINT "board_card_relation_card_id_kind_other_id_pk" PRIMARY KEY("card_id","kind","other_id")
);
--> statement-breakpoint
CREATE TABLE "board_input_seen" (
	"board_id" text NOT NULL,
	"connection_scope" text NOT NULL,
	"connection_id" text NOT NULL,
	"external_id" text NOT NULL,
	"first_seen_at" timestamp with time zone NOT NULL,
	CONSTRAINT "board_input_seen_board_id_connection_scope_connection_id_external_id_pk" PRIMARY KEY("board_id","connection_scope","connection_id","external_id")
);
--> statement-breakpoint
CREATE TABLE "board_stage" (
	"id" text PRIMARY KEY NOT NULL,
	"board_id" text NOT NULL,
	"name" text NOT NULL,
	"position" integer NOT NULL
);
--> statement-breakpoint
CREATE TABLE "board_stage_input" (
	"id" text PRIMARY KEY NOT NULL,
	"stage_id" text NOT NULL,
	"connection_scope" text NOT NULL,
	"connection_id" text NOT NULL,
	"query" text NOT NULL,
	"interval_seconds" integer DEFAULT 300 NOT NULL,
	"last_polled_at" timestamp with time zone,
	"last_ok_at" timestamp with time zone,
	"last_error" text DEFAULT '' NOT NULL,
	"lease_until" timestamp with time zone
);
--> statement-breakpoint
CREATE TABLE "board_stage_output" (
	"id" text PRIMARY KEY NOT NULL,
	"stage_id" text NOT NULL,
	"connection_scope" text NOT NULL,
	"connection_id" text NOT NULL,
	"fields" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"comment" text DEFAULT '' NOT NULL
);
--> statement-breakpoint
CREATE TABLE "project" (
	"id" text PRIMARY KEY NOT NULL,
	"scope" text NOT NULL,
	"owner_id" text,
	"ticker" text,
	"name" text,
	"description" text,
	"logo" text,
	"color" text,
	"connection_scope" text DEFAULT '' NOT NULL,
	"connection_id" text DEFAULT '' NOT NULL,
	"external_key" text DEFAULT '' NOT NULL,
	"provider_values" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"next_number" integer DEFAULT 1 NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "project_share" (
	"project_id" text NOT NULL,
	"subject" text NOT NULL,
	"subject_id" text DEFAULT '' NOT NULL,
	"level" text NOT NULL,
	CONSTRAINT "project_share_project_id_subject_subject_id_pk" PRIMARY KEY("project_id","subject","subject_id")
);
--> statement-breakpoint
CREATE TABLE "saved_view" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_id" text NOT NULL,
	"name" text NOT NULL,
	"filter" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "tracker_connection" (
	"id" text PRIMARY KEY NOT NULL,
	"provider" text NOT NULL,
	"name" text NOT NULL,
	"base_url" text NOT NULL,
	"auth" text NOT NULL,
	"login" text DEFAULT '' NOT NULL,
	"oauth_client_id" text DEFAULT '' NOT NULL,
	"secrets_enc" text DEFAULT '' NOT NULL,
	"enabled" boolean DEFAULT true NOT NULL,
	"allow_private_network" boolean DEFAULT false NOT NULL,
	"knowledge_enabled" boolean DEFAULT false NOT NULL,
	"knowledge_query" text DEFAULT '' NOT NULL,
	"healthy" boolean DEFAULT false NOT NULL,
	"last_error" text DEFAULT '' NOT NULL,
	"last_checked_at" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"updated_by" text DEFAULT '' NOT NULL
);
--> statement-breakpoint
ALTER TABLE "board" ADD CONSTRAINT "board_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board" ADD CONSTRAINT "board_owner_id_user_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card" ADD CONSTRAINT "board_card_board_id_board_id_fk" FOREIGN KEY ("board_id") REFERENCES "public"."board"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card" ADD CONSTRAINT "board_card_stage_id_board_stage_id_fk" FOREIGN KEY ("stage_id") REFERENCES "public"."board_stage"("id") ON DELETE no action ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card" ADD CONSTRAINT "board_card_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card_activity" ADD CONSTRAINT "board_card_activity_card_id_board_card_id_fk" FOREIGN KEY ("card_id") REFERENCES "public"."board_card"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card_comment" ADD CONSTRAINT "board_card_comment_card_id_board_card_id_fk" FOREIGN KEY ("card_id") REFERENCES "public"."board_card"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card_output" ADD CONSTRAINT "board_card_output_card_id_board_card_id_fk" FOREIGN KEY ("card_id") REFERENCES "public"."board_card"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card_relation" ADD CONSTRAINT "board_card_relation_card_id_board_card_id_fk" FOREIGN KEY ("card_id") REFERENCES "public"."board_card"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_card_relation" ADD CONSTRAINT "board_card_relation_other_id_board_card_id_fk" FOREIGN KEY ("other_id") REFERENCES "public"."board_card"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_input_seen" ADD CONSTRAINT "board_input_seen_board_id_board_id_fk" FOREIGN KEY ("board_id") REFERENCES "public"."board"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_stage" ADD CONSTRAINT "board_stage_board_id_board_id_fk" FOREIGN KEY ("board_id") REFERENCES "public"."board"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_stage_input" ADD CONSTRAINT "board_stage_input_stage_id_board_stage_id_fk" FOREIGN KEY ("stage_id") REFERENCES "public"."board_stage"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "board_stage_output" ADD CONSTRAINT "board_stage_output_stage_id_board_stage_id_fk" FOREIGN KEY ("stage_id") REFERENCES "public"."board_stage"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "project" ADD CONSTRAINT "project_owner_id_user_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "project_share" ADD CONSTRAINT "project_share_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "saved_view" ADD CONSTRAINT "saved_view_owner_id_user_id_fk" FOREIGN KEY ("owner_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "board_project_idx" ON "board" USING btree ("project_id","scope","owner_id");--> statement-breakpoint
CREATE INDEX "board_card_stage_idx" ON "board_card" USING btree ("stage_id","position");--> statement-breakpoint
CREATE INDEX "board_card_board_idx" ON "board_card" USING btree ("board_id","updated_at");--> statement-breakpoint
CREATE INDEX "board_card_link_idx" ON "board_card" USING btree ("connection_scope","connection_id","external_checked_at");--> statement-breakpoint
CREATE INDEX "board_card_sessions_idx" ON "board_card" USING gin ("session_ids");--> statement-breakpoint
CREATE UNIQUE INDEX "board_card_number_idx" ON "board_card" USING btree ("project_id","number");--> statement-breakpoint
CREATE INDEX "board_card_project_idx" ON "board_card" USING btree ("project_id","updated_at");--> statement-breakpoint
CREATE INDEX "board_card_activity_card_idx" ON "board_card_activity" USING btree ("card_id","created_at");--> statement-breakpoint
CREATE INDEX "board_card_comment_card_idx" ON "board_card_comment" USING btree ("card_id","created_at");--> statement-breakpoint
CREATE INDEX "board_card_output_card_idx" ON "board_card_output" USING btree ("card_id","created_at");--> statement-breakpoint
CREATE INDEX "board_card_output_due_idx" ON "board_card_output" USING btree ("status","connection_scope","next_attempt_at");--> statement-breakpoint
CREATE INDEX "board_card_relation_other_idx" ON "board_card_relation" USING btree ("other_id");--> statement-breakpoint
CREATE INDEX "board_stage_board_idx" ON "board_stage" USING btree ("board_id","position");--> statement-breakpoint
CREATE UNIQUE INDEX "board_stage_input_conn_idx" ON "board_stage_input" USING btree ("stage_id","connection_scope","connection_id");--> statement-breakpoint
CREATE INDEX "board_stage_input_due_idx" ON "board_stage_input" USING btree ("connection_scope","connection_id");--> statement-breakpoint
CREATE UNIQUE INDEX "board_stage_output_conn_idx" ON "board_stage_output" USING btree ("stage_id","connection_scope","connection_id");--> statement-breakpoint
CREATE INDEX "project_scope_owner_idx" ON "project" USING btree ("scope","owner_id");--> statement-breakpoint
CREATE INDEX "project_share_subject_idx" ON "project_share" USING btree ("subject","subject_id");--> statement-breakpoint
CREATE INDEX "saved_view_owner_idx" ON "saved_view" USING btree ("owner_id");