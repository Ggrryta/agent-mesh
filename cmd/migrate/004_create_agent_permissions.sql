-- A2A Agent 权限表 + 申请表
-- 创建时间：2026-04-21

CREATE TABLE IF NOT EXISTS agent_permissions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    agent_id VARCHAR(128) NOT NULL COMMENT '关联 agents.agent_id',
    owner_app_id VARCHAR(128) NOT NULL COMMENT 'Agent 所有者 AppID',
    consumer_app_id VARCHAR(128) NOT NULL COMMENT '被授权的 Consumer AppID',
    granted_at DATETIME NOT NULL COMMENT '授权时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',

    UNIQUE KEY uk_agent_consumer (agent_id, consumer_app_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='A2A Agent 调用权限表';

CREATE TABLE IF NOT EXISTS agent_applies (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    agent_id VARCHAR(128) NOT NULL COMMENT '申请调用的 Agent ID',
    owner_app_id VARCHAR(128) NOT NULL COMMENT 'Agent 所有者 AppID',
    applicant_app_id VARCHAR(128) NOT NULL COMMENT '申请者 AppID',
    reason VARCHAR(512) DEFAULT '' COMMENT '申请理由',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '状态：1=Pending 2=Approved 3=Rejected',
    reviewed_at DATETIME NULL COMMENT '审批时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',

    INDEX idx_agent_id (agent_id),
    INDEX idx_owner_app_id (owner_app_id),
    INDEX idx_applicant_app_id (applicant_app_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='A2A Agent 权限申请表';
