-- A2A Agent 注册表
-- 创建时间：2026-04-21

CREATE TABLE IF NOT EXISTS agents (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    agent_id VARCHAR(128) NOT NULL COMMENT 'Agent 唯一标识，如 weather-agent',
    name VARCHAR(128) NOT NULL COMMENT 'Agent 名称',
    description TEXT COMMENT 'Agent 描述',
    url VARCHAR(512) NOT NULL COMMENT '上游 Agent 的 base URL',
    version VARCHAR(64) DEFAULT '' COMMENT 'Agent 版本',
    owner_app_id VARCHAR(128) NOT NULL COMMENT '注册者的 Consumer AppID',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '状态：0=Inactive 1=Active 2=Draining',
    supports_streaming TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否支持流式响应',
    supports_push_notifications TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否支持推送通知',
    agent_card_json JSON COMMENT '完整 AgentCard JSON',
    last_heartbeat_at DATETIME NULL COMMENT '最后心跳时间',
    deleted_at DATETIME NULL COMMENT '软删除时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',

    UNIQUE KEY uk_agent_id (agent_id),
    INDEX idx_owner_app_id (owner_app_id),
    INDEX idx_status (status),
    INDEX idx_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='A2A Agent 注册表';
