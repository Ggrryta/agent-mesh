-- +goose Up
-- 给 agent 加 system_prompt：用户在前端配置的角色身份提示词。
-- 由 agent 大脑（Claude）启动时拉取，作为 LLM 调用的 system message。
ALTER TABLE agents ADD COLUMN system_prompt TEXT NULL AFTER agent_card_json;

-- +goose Down
ALTER TABLE agents DROP COLUMN system_prompt;
