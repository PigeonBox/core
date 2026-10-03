ALTER TABLE `transfer_logs` ADD COLUMN `api_key_id` integer;
CREATE INDEX `idx_transfer_logs_api_key_id` ON `transfer_logs`(`api_key_id`);
