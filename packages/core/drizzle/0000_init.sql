CREATE TABLE `access_token` (
	`token_hash` text PRIMARY KEY NOT NULL,
	`device_id` text NOT NULL,
	`user_id` text NOT NULL,
	`expires_at` integer NOT NULL,
	`created_at` integer NOT NULL,
	FOREIGN KEY (`device_id`) REFERENCES `device`(`id`) ON UPDATE no action ON DELETE cascade,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `access_token_device_idx` ON `access_token` (`device_id`);--> statement-breakpoint
CREATE TABLE `account` (
	`id` text PRIMARY KEY NOT NULL,
	`account_id` text NOT NULL,
	`provider_id` text NOT NULL,
	`user_id` text NOT NULL,
	`access_token` text,
	`refresh_token` text,
	`id_token` text,
	`access_token_expires_at` integer,
	`refresh_token_expires_at` integer,
	`scope` text,
	`password` text,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `account_user_idx` ON `account` (`user_id`);--> statement-breakpoint
CREATE TABLE `device` (
	`id` text PRIMARY KEY NOT NULL,
	`user_id` text NOT NULL,
	`name` text NOT NULL,
	`client_name` text,
	`os` text,
	`arch` text,
	`user_agent` text,
	`token_hash` text NOT NULL,
	`created_at` integer NOT NULL,
	`last_seen_at` integer,
	`revoked_at` integer,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `device_token_hash_unique` ON `device` (`token_hash`);--> statement-breakpoint
CREATE INDEX `device_user_idx` ON `device` (`user_id`);--> statement-breakpoint
CREATE TABLE `device_code` (
	`device_code_hash` text PRIMARY KEY NOT NULL,
	`user_code` text NOT NULL,
	`client_name` text,
	`device_name` text,
	`os` text,
	`arch` text,
	`status` text DEFAULT 'pending' NOT NULL,
	`user_id` text,
	`interval_sec` integer NOT NULL,
	`last_polled_at` integer,
	`expires_at` integer NOT NULL,
	`created_at` integer NOT NULL,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `device_code_user_code_unique` ON `device_code` (`user_code`);--> statement-breakpoint
CREATE TABLE `model` (
	`id` text PRIMARY KEY NOT NULL,
	`display_name` text NOT NULL,
	`provider_id` text NOT NULL,
	`upstream_model` text NOT NULL,
	`context_window` integer NOT NULL,
	`max_output_tokens` integer NOT NULL,
	`capabilities` text NOT NULL,
	`pricing` text,
	`fallbacks` text DEFAULT '[]' NOT NULL,
	`extra_body` text,
	`roles` text,
	`enabled` integer DEFAULT true NOT NULL,
	`sort_order` integer DEFAULT 0 NOT NULL,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL,
	FOREIGN KEY (`provider_id`) REFERENCES `provider`(`id`) ON UPDATE no action ON DELETE restrict
);
--> statement-breakpoint
CREATE TABLE `model_negotiation` (
	`model_id` text NOT NULL,
	`feature` text NOT NULL,
	`reason` text,
	`created_at` integer NOT NULL,
	PRIMARY KEY(`model_id`, `feature`),
	FOREIGN KEY (`model_id`) REFERENCES `model`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE TABLE `provider` (
	`id` text PRIMARY KEY NOT NULL,
	`type` text NOT NULL,
	`display_name` text,
	`base_url` text NOT NULL,
	`api_key_enc` text,
	`api_key_hint` text,
	`headers_enc` text,
	`allow_private_network` integer DEFAULT false NOT NULL,
	`enabled` integer DEFAULT true NOT NULL,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL
);
--> statement-breakpoint
CREATE TABLE `session` (
	`id` text PRIMARY KEY NOT NULL,
	`expires_at` integer NOT NULL,
	`token` text NOT NULL,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL,
	`ip_address` text,
	`user_agent` text,
	`user_id` text NOT NULL,
	`impersonated_by` text,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `session_token_unique` ON `session` (`token`);--> statement-breakpoint
CREATE INDEX `session_user_idx` ON `session` (`user_id`);--> statement-breakpoint
CREATE TABLE `setting` (
	`key` text PRIMARY KEY NOT NULL,
	`value` text NOT NULL,
	`updated_at` integer NOT NULL
);
--> statement-breakpoint
CREATE TABLE `usage_record` (
	`id` text PRIMARY KEY NOT NULL,
	`user_id` text NOT NULL,
	`device_id` text,
	`model_id` text NOT NULL,
	`provider_id` text,
	`upstream_model` text,
	`request_class` text,
	`session_id` text,
	`agent_id` text,
	`input_tokens` integer DEFAULT 0 NOT NULL,
	`output_tokens` integer DEFAULT 0 NOT NULL,
	`cache_read_tokens` integer DEFAULT 0 NOT NULL,
	`cache_write_tokens` integer DEFAULT 0 NOT NULL,
	`cost_usd` real DEFAULT 0 NOT NULL,
	`retries` integer DEFAULT 0 NOT NULL,
	`status` text NOT NULL,
	`stop_reason` text,
	`error_type` text,
	`duration_ms` integer,
	`created_at` integer NOT NULL,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `usage_user_created_idx` ON `usage_record` (`user_id`,`created_at`);--> statement-breakpoint
CREATE TABLE `user` (
	`id` text PRIMARY KEY NOT NULL,
	`name` text NOT NULL,
	`email` text NOT NULL,
	`email_verified` integer DEFAULT false NOT NULL,
	`image` text,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL,
	`role` text,
	`banned` integer DEFAULT false,
	`ban_reason` text,
	`ban_expires` integer
);
--> statement-breakpoint
CREATE UNIQUE INDEX `user_email_unique` ON `user` (`email`);--> statement-breakpoint
CREATE TABLE `verification` (
	`id` text PRIMARY KEY NOT NULL,
	`identifier` text NOT NULL,
	`value` text NOT NULL,
	`expires_at` integer NOT NULL,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL
);
