-- GAS Phase 1: agents 表增加 delivery_mode 字段
-- 0 = push (老 A2A HTTP server,保留兼容)
-- 1 = pull (GAS 模式,新默认)
-- 同时 url 对 pull 模式允许为空
-- 创建时间: 2026-05-09

ALTER TABLE agents
  ADD COLUMN delivery_mode TINYINT NOT NULL DEFAULT 1
    COMMENT '投递模式: 0=push(HTTP A2A), 1=pull(GAS Agent Core)'
  AFTER `supports_push_notifications`;

-- url 对 pull 模式允许为空(保留现有 NOT NULL 约束会阻碍 pull agent 注册)
ALTER TABLE agents
  MODIFY COLUMN url VARCHAR(512) DEFAULT NULL COMMENT 'A2A endpoint URL, pull 模式可为空';
