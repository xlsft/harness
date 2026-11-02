CREATE TABLE `sync_counter` (
	`name` text PRIMARY KEY NOT NULL,
	`value` integer NOT NULL
);
--> statement-breakpoint
CREATE TABLE `sync_document` (
	`id` text PRIMARY KEY NOT NULL,
	`owner_id` text NOT NULL,
	`scope` integer NOT NULL,
	`project_key` text DEFAULT '' NOT NULL,
	`kind` integer NOT NULL,
	`key` text NOT NULL,
	`version` integer NOT NULL,
	`content` text DEFAULT '' NOT NULL,
	`files` text DEFAULT '[]' NOT NULL,
	`content_hash` text NOT NULL,
	`source` text DEFAULT '' NOT NULL,
	`target_roles` text DEFAULT '[]' NOT NULL,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL,
	`updated_by` text NOT NULL,
	`deleted_at` integer
);
--> statement-breakpoint
CREATE UNIQUE INDEX `sync_document_key_idx` ON `sync_document` (`owner_id`,`scope`,`project_key`,`kind`,`key`);--> statement-breakpoint
CREATE INDEX `sync_document_version_idx` ON `sync_document` (`version`);--> statement-breakpoint
CREATE TABLE `sync_document_revision` (
	`document_id` text NOT NULL,
	`version` integer NOT NULL,
	`content` text NOT NULL,
	`files` text NOT NULL,
	`content_hash` text NOT NULL,
	`deleted` integer DEFAULT false NOT NULL,
	`updated_by` text NOT NULL,
	`created_at` integer NOT NULL,
	PRIMARY KEY(`document_id`, `version`)
);
--> statement-breakpoint
CREATE TABLE `sync_session` (
	`id` text PRIMARY KEY NOT NULL,
	`user_id` text NOT NULL,
	`info` text NOT NULL,
	`title` text DEFAULT '' NOT NULL,
	`cwd` text DEFAULT '' NOT NULL,
	`repo` text DEFAULT '' NOT NULL,
	`device_id` text DEFAULT '' NOT NULL,
	`status` integer DEFAULT 0 NOT NULL,
	`archived` integer DEFAULT false NOT NULL,
	`prompt_bundle_version` text DEFAULT '' NOT NULL,
	`last_seq` integer DEFAULT 0 NOT NULL,
	`created_at` integer NOT NULL,
	`last_activity_at` integer NOT NULL,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `sync_session_user_activity_idx` ON `sync_session` (`user_id`,`last_activity_at`);--> statement-breakpoint
CREATE TABLE `sync_session_entry` (
	`session_id` text NOT NULL,
	`seq` integer NOT NULL,
	`uuid` text NOT NULL,
	`agent_id` text DEFAULT '' NOT NULL,
	`parent_uuid` text DEFAULT '' NOT NULL,
	`type` text DEFAULT '' NOT NULL,
	`timestamp` text DEFAULT '' NOT NULL,
	`entry_json` text NOT NULL,
	`search_text` text DEFAULT '' NOT NULL,
	PRIMARY KEY(`session_id`, `seq`),
	FOREIGN KEY (`session_id`) REFERENCES `sync_session`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `sync_session_entry_uuid_idx` ON `sync_session_entry` (`session_id`,`agent_id`,`uuid`);--> statement-breakpoint
CREATE INDEX `usage_session_idx` ON `usage_record` (`session_id`);