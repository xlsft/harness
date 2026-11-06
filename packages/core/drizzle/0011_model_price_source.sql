ALTER TABLE "model" ADD COLUMN "pricing_source" text;--> statement-breakpoint
ALTER TABLE "model" ADD COLUMN "pricing_updated_at" timestamp with time zone;--> statement-breakpoint
ALTER TABLE "model" ADD COLUMN "price_change_held" jsonb;