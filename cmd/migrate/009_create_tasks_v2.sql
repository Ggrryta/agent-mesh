-- GAS Phase 1: Task 会话相关表
-- 重写 task 模型:一次 task 代表一次多轮对话
-- 创建时间: 2026-05-09
-- 注:原 async_tasks 表保留不动,作为 "push 模式异步调用" 的独立机制

CREATE TABLE IF NOT EXISTS tasks_v2 (
    task_id VARCHAR(64) PRIMARY KEY COMMENT 'task ID (UUID)',
    title VARCHAR(255) COMMENT '任务标题,便于用户识别',
    creator_agent_id VARCHAR(128) NOT NULL COMMENT '发起 agent',
    status VARCHAR(16) NOT NULL COMMENT '状态: active/closed/timeout/failed',
    expire_at DATETIME NULL COMMENT '过期时间,默认创建后 24h',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    closed_at DATETIME NULL COMMENT '关闭时间',

    INDEX idx_creator (creator_agent_id),
    INDEX idx_status (status),
    INDEX idx_expire (expire_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Task 会话';

CREATE TABLE IF NOT EXISTS task_members (
    task_id VARCHAR(64) NOT NULL COMMENT '归属 task',
    agent_id VARCHAR(128) NOT NULL COMMENT '成员 agent',
    role VARCHAR(16) NOT NULL DEFAULT 'member' COMMENT '角色: creator/member',
    joined_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '加入时间',
    left_at DATETIME NULL COMMENT '退出时间',
    last_read_seq INT NOT NULL DEFAULT 0 COMMENT '已读消息序号(用于未读计数)',

    PRIMARY KEY (task_id, agent_id),
    INDEX idx_agent (agent_id, left_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Task 成员';

CREATE TABLE IF NOT EXISTS task_messages (
    task_id VARCHAR(64) NOT NULL COMMENT '归属 task',
    seq INT NOT NULL COMMENT '消息序号,从 0 递增',
    sender_agent_id VARCHAR(128) NOT NULL COMMENT '发送方 agent',
    message_id VARCHAR(64) NOT NULL COMMENT '全局唯一消息 ID (A2A spec messageId)',
    content JSON NOT NULL COMMENT 'A2A Message parts 数组',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',

    PRIMARY KEY (task_id, seq),
    UNIQUE KEY uk_msg_id (message_id),
    INDEX idx_sender (sender_agent_id, created_at),
    INDEX idx_task_created (task_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Task 消息历史';
