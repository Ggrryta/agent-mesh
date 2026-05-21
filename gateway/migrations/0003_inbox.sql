-- +goose Up
-- +goose StatementBegin
--
-- 0003_inbox：每个 agent 一个 inbox，承载 task 相关事件的送达。
--
-- 设计对齐 ADR 010：
--   - inbox 是真相之源；agent 用 GET /v1/mesh/inbox?since=X 拉取
--   - delivered_at 只打 push 成功的标（观测用），pull 不清它
--   - kind 区分三类事件：message / artifact / transition
--   - payload_json 冗余存完整事件体，agent 拉一次拿全，不用再去 task_* 表 JOIN
--
CREATE TABLE inbox_events (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    agent_id      VARCHAR(64)  NOT NULL,            -- 收件人
    kind          VARCHAR(32)  NOT NULL,            -- message | artifact | transition
    task_id       VARCHAR(64)  NOT NULL,            -- 相关 task
    ref_id        VARCHAR(128) NOT NULL,            -- message_id / artifact_id / to_state
    payload_json  JSON         NOT NULL,            -- 事件完整负载
    created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    delivered_at  DATETIME(3)  NULL,                -- push 成功打标；pull 不更新
    -- 核心访问模式：agent 按自增 id 增量拉取。
    -- id 本身就是主键自带索引，(agent_id, id) 覆盖 WHERE agent_id=? AND id>?
    KEY idx_agent_id (agent_id, id),
    KEY idx_created (created_at)                    -- 将来 GC 用
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS inbox_events;
-- +goose StatementEnd
