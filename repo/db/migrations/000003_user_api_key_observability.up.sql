ALTER TABLE `user_api_keys` ADD COLUMN `last_used_ip` text;
ALTER TABLE `user_api_keys` ADD COLUMN `expiry_notified_at` datetime;
