-- +goose Up
-- 给 task_messages 加 preview 字段：agent 发消息时可选的摘要，让其他群成员
-- 在不读正文的情况下判断相关性（详见 Timeline 设计）。
ALTER TABLE task_messages ADD COLUMN preview VARCHAR(500) NULL AFTER parts_json;

-- +goose Down
ALTER TABLE task_messages DROP COLUMN preview;
