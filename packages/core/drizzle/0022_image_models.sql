ALTER TABLE "model" ADD COLUMN "kind" text DEFAULT 'chat' NOT NULL;--> statement-breakpoint
ALTER TABLE "usage_record" ADD COLUMN "images" integer DEFAULT 0 NOT NULL;