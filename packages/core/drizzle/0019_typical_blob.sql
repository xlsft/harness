CREATE TABLE "chat_bot" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"provider" integer NOT NULL,
	"enabled" boolean DEFAULT true NOT NULL,
	"bot_user_id" text NOT NULL,
	"bot_username" text DEFAULT '' NOT NULL,
	"delivery" integer DEFAULT 0 NOT NULL,
	"secrets_enc" text NOT NULL,
	"last_event_at" timestamp with time zone,
	"last_error" text DEFAULT '' NOT NULL,
	"created_at" timestamp with time zone NOT NULL,
	"updated_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
CREATE TABLE "chat_button" (
	"id" text PRIMARY KEY NOT NULL,
	"value" text NOT NULL,
	"created_at" timestamp with time zone NOT NULL
);
--> statement-breakpoint
ALTER TABLE "chat_app" ADD COLUMN "delivery" integer DEFAULT 0 NOT NULL;--> statement-breakpoint
ALTER TABLE "chat_thread" ADD COLUMN "app_key" text DEFAULT '' NOT NULL;--> statement-breakpoint
ALTER TABLE "chat_thread" ADD COLUMN "host_work_id" text DEFAULT '' NOT NULL;--> statement-breakpoint
ALTER TABLE "chat_bot" ADD CONSTRAINT "chat_bot_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "chat_bot_user_idx" ON "chat_bot" USING btree ("user_id","provider");--> statement-breakpoint
CREATE UNIQUE INDEX "chat_bot_bot_idx" ON "chat_bot" USING btree ("provider","bot_user_id");--> statement-breakpoint
CREATE INDEX "chat_button_created_idx" ON "chat_button" USING btree ("created_at");