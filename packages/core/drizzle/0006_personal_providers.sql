ALTER TABLE "model" DROP CONSTRAINT "model_provider_id_provider_id_fk";
--> statement-breakpoint
ALTER TABLE "provider" ADD COLUMN "owner_user_id" text;--> statement-breakpoint
ALTER TABLE "model" ADD CONSTRAINT "model_provider_id_provider_id_fk" FOREIGN KEY ("provider_id") REFERENCES "public"."provider"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "provider" ADD CONSTRAINT "provider_owner_user_id_user_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE INDEX "provider_owner_idx" ON "provider" USING btree ("owner_user_id");