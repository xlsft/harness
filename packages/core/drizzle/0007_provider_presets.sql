ALTER TABLE "provider" ADD COLUMN "kind" text DEFAULT 'llm' NOT NULL;--> statement-breakpoint
ALTER TABLE "provider" ADD COLUMN "preset_id" text;