-- +goose Up
-- 给 task 加闲聊连击检测字段，用于 auto-close 兜底。

ALTER TABLE reliable_async_tasks
  ADD COLUMN chat_streak INT NOT NULL DEFAULT 0 COMMENT '连续闲聊消息计数，实质消息重置为 0',
  ADD COLUMN last_substantive_at TIMESTAMP(3) NULL DEFAULT NULL COMMENT '最后一条实质性消息的时间';

CREATE INDEX idx_tasks_chat_streak ON reliable_async_tasks (status, chat_streak);

-- +goose Down
ALTER TABLE reliable_async_tasks
  DROP INDEX idx_tasks_chat_streak,
  DROP COLUMN chat_streak,
  DROP COLUMN last_substantive_at;
