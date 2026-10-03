DROP INDEX IF EXISTS `idx_transfer_logs_api_key_id`;
-- SQLite 3.35+ 支持 DROP COLUMN；旧库该列本就允许缺失
ALTER TABLE `transfer_logs` DROP COLUMN `api_key_id`;
