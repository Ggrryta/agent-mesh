-- Reliable async task fact table + outbox events.
-- This keeps Redis async mode intact and adds a durable path for reliability=reliable.

CREATE TABLE IF NOT EXISTS reliable_async_tasks (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    task_id VARCHAR(64) NOT NULL COMMENT '任务唯一 ID',
    agent_id VARCHAR(128) NOT NULL COMMENT '目标 Agent ID',
    skill_id VARCHAR(128) DEFAULT '' COMMENT '目标 Skill ID',
    app_id VARCHAR(128) NOT NULL COMMENT '调用方 AppID',
    input JSON NOT NULL COMMENT '任务输入',
    output JSON NULL COMMENT '任务输出',
    status VARCHAR(32) NOT NULL COMMENT 'pending/running/retrying/completed/failed',
    error_msg TEXT COMMENT '错误信息',
    retries INT NOT NULL DEFAULT 0 COMMENT '已重试次数',
    next_run_at DATETIME NULL COMMENT '下次执行时间',
    version BIGINT NOT NULL DEFAULT 0 COMMENT '乐观锁版本',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',

    UNIQUE KEY uk_task_id (task_id),
    INDEX idx_agent_id (agent_id),
    INDEX idx_app_id_created_at (app_id, created_at),
    INDEX idx_status_next_run (status, next_run_at),
    INDEX idx_updated_at (updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='可靠异步任务事实表';

CREATE TABLE IF NOT EXISTS outbox_events (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    event_id VARCHAR(64) NOT NULL COMMENT '事件唯一 ID',
    aggregate_type VARCHAR(64) NOT NULL COMMENT '聚合类型',
    aggregate_id VARCHAR(128) NOT NULL COMMENT '聚合 ID',
    event_type VARCHAR(128) NOT NULL COMMENT '事件类型',
    payload JSON NOT NULL COMMENT '事件载荷',
    status VARCHAR(32) NOT NULL DEFAULT 'pending' COMMENT 'pending/sent/failed',
    retries INT NOT NULL DEFAULT 0 COMMENT '发送重试次数',
    next_retry_at DATETIME NULL COMMENT '下次重试时间',
    sent_at DATETIME NULL COMMENT '发送成功时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',

    UNIQUE KEY uk_event_id (event_id),
    INDEX idx_status_next_retry (status, next_retry_at),
    INDEX idx_aggregate (aggregate_type, aggregate_id),
    INDEX idx_event_type (event_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Outbox 事件表';
