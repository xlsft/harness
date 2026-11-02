CREATE TABLE "mcp_audit" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"device_id" text,
	"server_id" text NOT NULL,
	"action" text NOT NULL,
	"config_hash" text DEFAULT '' NOT NULL,
	"detail" jsonb DEFAULT '{}'::jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "mcp_auth" (
	"user_id" text NOT NULL,
	"server_id" text NOT NULL,
	"state" text NOT NULL,
	"message" text DEFAULT '' NOT NULL,
	"tokens_enc" text,
	"issuer" text DEFAULT '' NOT NULL,
	"redirect_uri" text DEFAULT '' NOT NULL,
	"resource" text DEFAULT '' NOT NULL,
	"server_url" text DEFAULT '' NOT NULL,
	"requested_scopes" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"granted_scopes" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"account" text DEFAULT '' NOT NULL,
	"expires_at" timestamp with time zone,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "mcp_auth_user_id_server_id_pk" PRIMARY KEY("user_id","server_id")
);
--> statement-breakpoint
CREATE TABLE "mcp_catalog_snapshot" (
	"version" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"project_key" text DEFAULT '' NOT NULL,
	"servers" jsonb NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"last_used_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "mcp_device_status" (
	"device_id" text NOT NULL,
	"server_id" text NOT NULL,
	"user_id" text NOT NULL,
	"config_hash" text DEFAULT '' NOT NULL,
	"availability" integer DEFAULT 0 NOT NULL,
	"reason" text DEFAULT '' NOT NULL,
	"approval" integer DEFAULT 0 NOT NULL,
	"last_error" text DEFAULT '' NOT NULL,
	"tools" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"tools_reported_at" timestamp with time zone,
	"reported_at" timestamp with time zone NOT NULL,
	CONSTRAINT "mcp_device_status_device_id_server_id_pk" PRIMARY KEY("device_id","server_id")
);
--> statement-breakpoint
CREATE TABLE "mcp_oauth_client" (
	"server_id" text NOT NULL,
	"issuer" text NOT NULL,
	"redirect_uri" text NOT NULL,
	"client_enc" text NOT NULL,
	"metadata" jsonb,
	"dynamic" boolean DEFAULT false NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "mcp_oauth_client_server_id_issuer_redirect_uri_pk" PRIMARY KEY("server_id","issuer","redirect_uri")
);
--> statement-breakpoint
CREATE TABLE "mcp_oauth_flow" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"server_id" text NOT NULL,
	"state_hash" text NOT NULL,
	"data_enc" text NOT NULL,
	"return_url" text DEFAULT '' NOT NULL,
	"status" text DEFAULT 'pending' NOT NULL,
	"error" text DEFAULT '' NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"completed_at" timestamp with time zone,
	CONSTRAINT "mcp_oauth_flow_state_hash_unique" UNIQUE("state_hash")
);
--> statement-breakpoint
CREATE TABLE "mcp_secret" (
	"server_id" text NOT NULL,
	"name" text NOT NULL,
	"value_enc" text NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	CONSTRAINT "mcp_secret_server_id_name_pk" PRIMARY KEY("server_id","name")
);
--> statement-breakpoint
CREATE TABLE "mcp_server" (
	"id" text PRIMARY KEY NOT NULL,
	"owner_id" text NOT NULL,
	"scope" integer NOT NULL,
	"project_key" text DEFAULT '' NOT NULL,
	"name" text NOT NULL,
	"transport" integer NOT NULL,
	"enabled" boolean DEFAULT true NOT NULL,
	"always_load" boolean DEFAULT false NOT NULL,
	"config" jsonb NOT NULL,
	"target_roles" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"allow_private_network" boolean DEFAULT false NOT NULL,
	"version" bigint NOT NULL,
	"config_hash" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL,
	"updated_by" text NOT NULL
);
--> statement-breakpoint
CREATE TABLE "mcp_tool_cache" (
	"server_id" text NOT NULL,
	"user_id" text NOT NULL,
	"config_hash" text NOT NULL,
	"tools" jsonb NOT NULL,
	"fetched_at" timestamp with time zone NOT NULL,
	CONSTRAINT "mcp_tool_cache_server_id_user_id_pk" PRIMARY KEY("server_id","user_id")
);
--> statement-breakpoint
ALTER TABLE "mcp_auth" ADD CONSTRAINT "mcp_auth_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_auth" ADD CONSTRAINT "mcp_auth_server_id_mcp_server_id_fk" FOREIGN KEY ("server_id") REFERENCES "public"."mcp_server"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_catalog_snapshot" ADD CONSTRAINT "mcp_catalog_snapshot_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_device_status" ADD CONSTRAINT "mcp_device_status_device_id_device_id_fk" FOREIGN KEY ("device_id") REFERENCES "public"."device"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_device_status" ADD CONSTRAINT "mcp_device_status_server_id_mcp_server_id_fk" FOREIGN KEY ("server_id") REFERENCES "public"."mcp_server"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_oauth_client" ADD CONSTRAINT "mcp_oauth_client_server_id_mcp_server_id_fk" FOREIGN KEY ("server_id") REFERENCES "public"."mcp_server"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_oauth_flow" ADD CONSTRAINT "mcp_oauth_flow_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_oauth_flow" ADD CONSTRAINT "mcp_oauth_flow_server_id_mcp_server_id_fk" FOREIGN KEY ("server_id") REFERENCES "public"."mcp_server"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_secret" ADD CONSTRAINT "mcp_secret_server_id_mcp_server_id_fk" FOREIGN KEY ("server_id") REFERENCES "public"."mcp_server"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "mcp_tool_cache" ADD CONSTRAINT "mcp_tool_cache_server_id_mcp_server_id_fk" FOREIGN KEY ("server_id") REFERENCES "public"."mcp_server"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "mcp_audit_server_idx" ON "mcp_audit" USING btree ("server_id","created_at");--> statement-breakpoint
CREATE INDEX "mcp_audit_user_idx" ON "mcp_audit" USING btree ("user_id","created_at");--> statement-breakpoint
CREATE INDEX "mcp_catalog_snapshot_used_idx" ON "mcp_catalog_snapshot" USING btree ("last_used_at");--> statement-breakpoint
CREATE INDEX "mcp_device_status_server_idx" ON "mcp_device_status" USING btree ("server_id");--> statement-breakpoint
CREATE INDEX "mcp_oauth_flow_user_idx" ON "mcp_oauth_flow" USING btree ("user_id");--> statement-breakpoint
CREATE UNIQUE INDEX "mcp_server_name_idx" ON "mcp_server" USING btree ("owner_id","scope","project_key","name");