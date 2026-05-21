-- +goose Up
-- 死信表：存储重试 10 次仍失败的 outbox 事件，供人工排查和手动重试。

CREATE TABLE outbox_dead_letters (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    original_id    BIGINT UNSIGNED NOT NULL,          -- 原 outbox_events.id
    event_id       VARCHAR(64) NOT NULL,
    event_type     VARCHAR(64) NOT NULL,
    payload        JSON NOT NULL,
    error_msg      VARCHAR(1024) NOT NULL DEFAULT '',  -- 最后一次失败的错误信息
    retries        INT NOT NULL DEFAULT 0,
    original_created_at DATETIME(3) NOT NULL,          -- 原始事件创建时间
    dead_at        DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),  -- 进入死信的时间
    resolved_at    DATETIME(3) NULL,                   -- 人工处理后标记
    resolved_by    VARCHAR(64) NULL,                   -- 处理人
    KEY idx_event_type (event_type),
    KEY idx_dead_at (dead_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE IF EXISTS outbox_dead_letters;
