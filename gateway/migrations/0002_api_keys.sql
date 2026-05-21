-- +goose Up
-- +goose StatementBegin
--
-- 0002_api_keys：用户持有的长期凭证，用于跟 Gateway 换短期 JWT。
--
-- 设计要点（详见 ADR 007）：
--   - 属于用户，不绑 agent；用户自己决定分发给哪些 agent
--   - SHA-256(raw) 存 hash，key 本身是 256-bit 随机足够抗爆破
--   - 不硬过期；吊销靠 revoked_at 软删除
--   - key_prefix 展示给 UI，让用户区分同一个账号下的多把 key
--
CREATE TABLE api_keys (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    owner_uid     BIGINT UNSIGNED NOT NULL,        -- 属于哪个用户
    key_prefix    VARCHAR(20)  NOT NULL,           -- 原始 key 的前 16 字符，仅展示
    key_hash      CHAR(64)     NOT NULL,           -- SHA-256(raw key) 的 hex
    label         VARCHAR(64)  NOT NULL DEFAULT '',-- 用户起的名字，如 "CI" / "本机 alice"
    last_used_at  DATETIME(3)  NULL,               -- 最近一次换 JWT 的时间（异步更新）
    revoked_at    DATETIME(3)  NULL,               -- 软删除；非 NULL = 已吊销
    created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_key_hash (key_hash),
    KEY idx_owner_active (owner_uid, revoked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS api_keys;
-- +goose StatementEnd
