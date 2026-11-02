CREATE TABLE "relay_command" (
	"id" text PRIMARY KEY NOT NULL,
	"ord" bigserial NOT NULL,
	"user_id" text NOT NULL,
	"host_id" text DEFAULT '' NOT NULL,
	"session_id" text DEFAULT '' NOT NULL,
	"kind" text NOT NULL,
	"command" text NOT NULL,
	"idempotency_key" text,
	"state" text DEFAULT 'queued' NOT NULL,
	"ack" text,
	"created_at" timestamp with time zone NOT NULL,
	"expires_at" timestamp with time zone NOT NULL,
	"acked_at" timestamp with time zone
);
--> statement-breakpoint
CREATE TABLE "relay_event" (
	"session_id" text NOT NULL,
	"seq" bigint NOT NULL,
	"uuid" text NOT NULL,
	"epoch" bigint NOT NULL,
	"event" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	CONSTRAINT "relay_event_session_id_seq_pk" PRIMARY KEY("session_id","seq")
);
--> statement-breakpoint
CREATE TABLE "relay_host" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"device_id" text NOT NULL,
	"instance_id" text NOT NULL,
	"client_version" text DEFAULT '' NOT NULL,
	"daemon" boolean DEFAULT false NOT NULL,
	"allowed_roots" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"tunnels" boolean DEFAULT false NOT NULL,
	"pty" boolean DEFAULT false NOT NULL,
	"last_seen_at" timestamp with time zone NOT NULL,
	"last_local_activity_at" timestamp with time zone,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "relay_session" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"host_id" text NOT NULL,
	"device_id" text NOT NULL,
	"epoch" bigint NOT NULL,
	"acked_seq" bigint DEFAULT 0 NOT NULL,
	"info" jsonb NOT NULL,
	"status" integer DEFAULT 0 NOT NULL,
	"active" boolean DEFAULT true NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "relay_command" ADD CONSTRAINT "relay_command_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "relay_event" ADD CONSTRAINT "relay_event_session_id_relay_session_id_fk" FOREIGN KEY ("session_id") REFERENCES "public"."relay_session"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "relay_host" ADD CONSTRAINT "relay_host_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "relay_host" ADD CONSTRAINT "relay_host_device_id_device_id_fk" FOREIGN KEY ("device_id") REFERENCES "public"."device"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "relay_session" ADD CONSTRAINT "relay_session_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "relay_command_idem_idx" ON "relay_command" USING btree ("user_id","idempotency_key");--> statement-breakpoint
CREATE INDEX "relay_command_session_idx" ON "relay_command" USING btree ("session_id","state");--> statement-breakpoint
CREATE INDEX "relay_command_host_idx" ON "relay_command" USING btree ("host_id","state");--> statement-breakpoint
CREATE UNIQUE INDEX "relay_event_uuid_idx" ON "relay_event" USING btree ("session_id","uuid");--> statement-breakpoint
CREATE UNIQUE INDEX "relay_host_instance_idx" ON "relay_host" USING btree ("device_id","instance_id");--> statement-breakpoint
CREATE INDEX "relay_host_user_idx" ON "relay_host" USING btree ("user_id");--> statement-breakpoint
CREATE INDEX "relay_session_host_idx" ON "relay_session" USING btree ("host_id");--> statement-breakpoint
CREATE INDEX "relay_session_user_idx" ON "relay_session" USING btree ("user_id");