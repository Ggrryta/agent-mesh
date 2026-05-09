-- GAS Phase 1: Friendship 表
-- Agent 间对称好友关系,本期本期粗粒度(好友即全 skill 通达)
-- agent_a_id 和 agent_b_id 按字典序规范化存储,保证 (A,B) 和 (B,A) 只存一行
-- 创建时间: 2026-05-09

CREATE TABLE IF NOT EXISTS friendships (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    agent_a_id VARCHAR(128) NOT NULL COMMENT '好友对中字典序较小的一方',
    agent_b_id VARCHAR(128) NOT NULL COMMENT '好友对中字典序较大的一方',
    status VARCHAR(16) NOT NULL COMMENT '状态: pending/accepted/rejected/revoked',
    initiator_id VARCHAR(128) NOT NULL COMMENT '发起加好友请求的一方',
    reason TEXT COMMENT '加好友理由',
    accepted_at DATETIME NULL COMMENT '接受时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',

    UNIQUE KEY uk_pair (agent_a_id, agent_b_id),
    INDEX idx_a_status (agent_a_id, status),
    INDEX idx_b_status (agent_b_id, status),
    INDEX idx_initiator (initiator_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Agent 好友关系(对称)';
