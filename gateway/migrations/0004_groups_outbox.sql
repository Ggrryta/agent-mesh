-- +goose Up
CREATE TABLE IF NOT EXISTS `groups` (
    `id` BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    `group_id` VARCHAR(64) NOT NULL,
    `context_id` VARCHAR(64) NOT NULL COMMENT '1:1 关联 A2A context',
    `name` VARCHAR(128) NOT NULL DEFAULT '',
    `owner_uid` BIGINT NOT NULL,
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY `uk_group_id` (`group_id`),
    UNIQUE KEY `uk_context_id` (`context_id`),
    KEY `idx_owner` (`owner_uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `group_members` (
    `id` BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    `group_id` VARCHAR(64) NOT NULL,
    `agent_id` VARCHAR(64) NOT NULL,
    `role` VARCHAR(16) NOT NULL DEFAULT 'member' COMMENT 'owner|admin|member',
    `joined_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY `uk_group_agent` (`group_id`, `agent_id`),
    KEY `idx_agent` (`agent_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `outbox_events` (
    `id` BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    `event_type` VARCHAR(64) NOT NULL,
    `payload_json` JSON NOT NULL,
    `status` VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending|sent|failed',
    `retries` INT NOT NULL DEFAULT 0,
    `next_retry_at` DATETIME(3) NULL,
    `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    `sent_at` DATETIME(3) NULL,
    KEY `idx_status_retry` (`status`, `next_retry_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- +goose Down
DROP TABLE IF EXISTS `outbox_events`;
DROP TABLE IF EXISTS `group_members`;
DROP TABLE IF EXISTS `groups`;
