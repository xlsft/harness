ALTER TABLE "device" ADD COLUMN "fingerprint" text;--> statement-breakpoint
ALTER TABLE "device_code" ADD COLUMN "fingerprint" text;--> statement-breakpoint
CREATE INDEX "device_fingerprint_idx" ON "device" USING btree ("user_id","fingerprint");