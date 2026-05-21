-- +goose Up
-- Dispatcher 多实例支持：按 agent 分片 + Pod 注册。

-- 1. outbox_events 加 target_agent_id 计算列（从 event_type 提取目标 agent）
ALTER TABLE outbox_events
  ADD COLUMN target_agent_id VARCHAR(64) GENERATED ALWAYS AS
    (SUBSTRING_INDEX(event_type, ':', -1)) STORED AFTER event_type,
  ADD KEY idx_target_pending (target_agent_id, status, id);

-- 2. Pod 注册表（dispatcher 实例心跳）
CREATE TABLE dispatcher_pods (
  pod_id VARCHAR(64) NOT NULL PRIMARY KEY,
  pod_index INT NOT NULL DEFAULT 0,
  heartbeat_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_heartbeat (heartbeat_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
ALTER TABLE outbox_events DROP KEY idx_target_pending, DROP COLUMN target_agent_id;
DROP TABLE IF EXISTS dispatcher_pods;
