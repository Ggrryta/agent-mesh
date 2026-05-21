-- +goose Up
-- 给 agents 表加 headline 和 workspace_path 字段。
-- headline: 一句话摘要，注入到其他 agent 的 system prompt。
-- workspace_path: agent 的工作目录路径，meshd 启动 worker 时用作 cwd。

ALTER TABLE agents ADD COLUMN headline VARCHAR(200) DEFAULT NULL AFTER description;
ALTER TABLE agents ADD COLUMN workspace_path VARCHAR(512) DEFAULT NULL AFTER system_prompt;

-- +goose Down
ALTER TABLE agents DROP COLUMN headline;
ALTER TABLE agents DROP COLUMN workspace_path;
