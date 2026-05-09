-- A2A Agent Skill 表（Agent 声明的能力单元）
-- 创建时间：2026-04-21

CREATE TABLE IF NOT EXISTS agent_skills (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    agent_id VARCHAR(128) NOT NULL COMMENT '关联 agents.agent_id',
    skill_id VARCHAR(128) NOT NULL COMMENT 'Agent 内部 skill 标识',
    name VARCHAR(128) NOT NULL COMMENT 'Skill 名称',
    description TEXT COMMENT 'Skill 描述',
    tags JSON COMMENT '标签数组，用于能力搜索，如 ["weather","forecast"]',
    input_modes JSON COMMENT '支持的输入模态，如 ["text","data"]',
    output_modes JSON COMMENT '支持的输出模态，如 ["text"]',

    INDEX idx_agent_id (agent_id),
    UNIQUE KEY uk_agent_skill (agent_id, skill_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='A2A Agent Skill 表';
