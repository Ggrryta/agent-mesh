-- +goose Up
-- Market：用户可以把自己的 agent 发布成 publication，别人 fork 一份到自己名下。
-- 核心字段：title / summary / system_prompt_template / category / tags
-- 不直接关联到 agents.id —— 发布时把当时的 prompt 模板"快照"进 publication_row，
-- 这样发布者改 system_prompt 不会影响已发布的版本，符合"市场上展示的是稳定版本"语义。
CREATE TABLE agent_publications (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    publisher_uid BIGINT NOT NULL COMMENT '发布者 user uid',
    source_agent_id VARCHAR(64) NOT NULL COMMENT '发布时的源 agent_id',
    title VARCHAR(120) NOT NULL,
    summary VARCHAR(500) NOT NULL DEFAULT '',
    system_prompt_template TEXT NULL COMMENT '快照自源 agent 的 system_prompt',
    category VARCHAR(40) NOT NULL DEFAULT '' COMMENT 'general / coding / writing / ...',
    tags VARCHAR(200) NOT NULL DEFAULT '' COMMENT '逗号分隔',
    download_count INT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_publisher (publisher_uid),
    KEY idx_category (category),
    KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 订阅记录：用户 fork 了哪个 publication 到哪个新 agent_id。
-- 用于在 market 列表里标"已添加"，以及统计 download_count。
CREATE TABLE agent_subscriptions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    uid BIGINT NOT NULL COMMENT 'fork 的用户',
    publication_id BIGINT UNSIGNED NOT NULL,
    forked_agent_id VARCHAR(64) NOT NULL COMMENT '用户名下新建的 agent_id',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uniq_user_pub (uid, publication_id),
    KEY idx_publication (publication_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE agent_subscriptions;
DROP TABLE agent_publications;
