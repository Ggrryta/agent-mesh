-- +goose Up
-- +goose StatementBegin
--
-- 0001_init: Agent-Mesh MVP 的基线 schema。
--
-- 多张表在一个 migration 里建——因为它们互相引用，拆开会让 up/down 的
-- 噪声大而无益。上线后新的改动各自用独立编号的文件。
--

-- users：注册的人类账号，拥有自己的 agent。
CREATE TABLE users (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username      VARCHAR(64)  NOT NULL,
    password_hash VARCHAR(128) NOT NULL,
    -- 每个用户一枚自动创建的 virtual-user agent；允许 NULL 便于在 users
    -- 行插入后再回填。
    virtual_user_agent_id VARCHAR(64) DEFAULT NULL,
    created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- agents：mesh 的通信节点。要么是用户拥有的 "normal" agent，
-- 要么是自动创建的 "virtual-user" agent（在 mesh 里代表其 owner）。
CREATE TABLE agents (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    agent_id      VARCHAR(64)  NOT NULL,           -- 全局唯一标识
    owner_uid     BIGINT UNSIGNED NOT NULL,
    name          VARCHAR(128) NOT NULL,
    description   VARCHAR(512) NOT NULL DEFAULT '',
    url           VARCHAR(256) NOT NULL DEFAULT '', -- agent 的 A2A 端点（prober 用）；virtual-user 留空
    version       VARCHAR(64)  NOT NULL DEFAULT '',
    kind          VARCHAR(32)  NOT NULL DEFAULT 'normal',  -- normal | virtual-user
    status        VARCHAR(32)  NOT NULL DEFAULT 'active',  -- active | draining | inactive
    agent_card_json JSON NULL,
    last_heartbeat_at DATETIME(3) NULL,
    -- 被并发安全的 Prober 用: UPDATE ... WHERE last_probed_at < now - N。
    last_probed_at    DATETIME(3) NULL,
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_agent_id (agent_id),
    KEY idx_owner (owner_uid),
    KEY idx_status (status),
    KEY idx_last_probed_at (last_probed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- skills：agent 对外声明的能力。每次 Register 整组替换，不保留
-- (agent_id, skill_id) 的演化历史。
CREATE TABLE skills (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    agent_id      VARCHAR(64)  NOT NULL,
    skill_id      VARCHAR(128) NOT NULL,
    name          VARCHAR(128) NOT NULL,
    description   VARCHAR(512) NOT NULL DEFAULT '',
    tags_json     JSON NULL,
    input_modes_json  JSON NULL,
    output_modes_json JSON NULL,
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_agent_skill (agent_id, skill_id),
    KEY idx_agent (agent_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- friendships：对等通信授权。
-- 行里仅 from/to 区分"谁发起了请求"；一旦 accepted，双向都放行。
CREATE TABLE friendships (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    from_agent_id VARCHAR(64) NOT NULL,
    to_agent_id   VARCHAR(64) NOT NULL,
    status        VARCHAR(32) NOT NULL DEFAULT 'pending', -- pending|accepted|rejected|revoked
    reason        VARCHAR(512) NOT NULL DEFAULT '',
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    -- 每对 (from, to) 只能有一条 pending。accepted / rejected / revoked 可以
    -- 和新的 pending 共存，所以通过计算列式的部分唯一性来约束，而不是单纯
    -- 依赖这条 unique key。
    UNIQUE KEY uk_pair (from_agent_id, to_agent_id),
    KEY idx_from (from_agent_id),
    KEY idx_to   (to_agent_id),
    KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- reliable_async_tasks：对齐 A2A 的 Task 事实行。一行 = 一个带生命周期的
-- 工作单元。中间对话轮次进 task_messages；可交付物进 task_artifacts。
-- 详见 ADR 004。
CREATE TABLE reliable_async_tasks (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    task_id       VARCHAR(64) NOT NULL,         -- A2A Task.id
    context_id    VARCHAR(64) NOT NULL,         -- A2A contextId：把相关 task 归到一次会话
    from_agent_id VARCHAR(64) NOT NULL,         -- 发起方 client agent
    to_agent_id   VARCHAR(64) NOT NULL,         -- 服务方 agent
    -- A2A TaskState: submitted | working | input-required | auth-required |
    -- completed | canceled | failed | rejected
    -- （本项目内部额外瞬态：retrying）
    status        VARCHAR(32) NOT NULL DEFAULT 'submitted',
    -- A2A TaskStatus.message 是"最新一条状态消息"；这里存为快照。
    -- 完整历史查 task_messages。
    status_message VARCHAR(1024) NOT NULL DEFAULT '',
    error_msg     VARCHAR(1024) NOT NULL DEFAULT '',
    -- Worker 侧调度簿记（非 A2A 字段）：
    retries       INT NOT NULL DEFAULT 0,
    next_run_at   DATETIME(3) NULL,
    claimed_at    DATETIME(3) NULL,   -- worker 抢到的时间；僵尸回收用
    version       INT NOT NULL DEFAULT 0,
    -- 元数据（可选，对应 A2A Task.metadata）：
    metadata_json JSON NULL,
    created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_task_id (task_id),
    KEY idx_context_id (context_id),                  -- 按 context 聚合相关 task
    KEY idx_to_status (to_agent_id, status),          -- Worker：查"派给我的 task"
    KEY idx_status_nextrun (status, next_run_at)      -- Worker：扫可跑的 task
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- task_messages：A2A Task.history[]。一个 Task 生命周期里每一次 user /
-- agent 的发言。取代旧版 reliable_async_tasks 里扁平的 `input JSON` 字段。
-- 支持 input-required 续约（client 再 append 一条 ROLE_USER 消息）和流式
-- agent 进度更新。
CREATE TABLE task_messages (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    message_id    VARCHAR(64)  NOT NULL,              -- A2A Message.messageId
    task_id       VARCHAR(64)  NOT NULL,              -- 类 FK，为了性能不强约束
    context_id    VARCHAR(64)  NOT NULL,              -- 冗余，方便按 context 扫描
    role          VARCHAR(16)  NOT NULL,              -- user | agent
    -- A2A Message.parts[]：以 JSON 数组存储 Part 对象。
    -- 每个 Part 是 oneof {text, raw, url, data} + 可选 filename / mediaType / metadata。
    parts_json    JSON         NOT NULL,
    -- A2A Message.metadata（可选）：
    metadata_json JSON         NULL,
    -- A2A Message.referenceTaskIds（可选，follow-up 请求用）：
    reference_task_ids JSON    NULL,
    created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_message_id (message_id),
    KEY idx_task_created (task_id, created_at),        -- 按顺序载入某个 task 的 history
    KEY idx_context_created (context_id, created_at)   -- 按 context 载入整串消息
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- task_artifacts：A2A Task.artifacts[]。服务方 agent 产出的可交付物。
-- 一个 Task 可以产出多个 artifact；artifact 支持增量流式——后续行如果复用
-- 同一个 `name`，客户端视为新版本。
CREATE TABLE task_artifacts (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    artifact_id   VARCHAR(64)  NOT NULL,              -- A2A Artifact.artifactId；task 内唯一
    task_id       VARCHAR(64)  NOT NULL,
    context_id    VARCHAR(64)  NOT NULL,              -- 冗余，加速查询
    name          VARCHAR(128) NOT NULL DEFAULT '',   -- 跨 task 精炼时保持稳定
    description   VARCHAR(512) NOT NULL DEFAULT '',
    -- A2A Artifact.parts[]:
    parts_json    JSON         NOT NULL,
    metadata_json JSON         NULL,
    created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_task_artifact (task_id, artifact_id),
    KEY idx_task (task_id),
    KEY idx_context_name (context_id, name)            -- follow-up：在 context 里找同名最新 artifact
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- outbox_events：事务型 outbox，保证"任务状态"和"事件发布"最终一致。
-- Dispatcher 扫表并把事件 publish 到 MQ 总线。
CREATE TABLE outbox_events (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    event_id      VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(64) NOT NULL, -- 例如 'task'
    aggregate_id   VARCHAR(64) NOT NULL, -- 例如 task_id
    event_type     VARCHAR(64) NOT NULL, -- 例如 'task.created'
    payload        JSON NOT NULL,
    status         VARCHAR(32) NOT NULL DEFAULT 'pending', -- pending|sending|sent|failed
    retries        INT NOT NULL DEFAULT 0,
    next_run_at    DATETIME(3) NULL,
    created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_event_id (event_id),
    KEY idx_status_nextrun (status, next_run_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- configs：集中存放可热更的 gateway 配置。Dispatcher 在插入时触发
-- Redis Pub/Sub 通知。
CREATE TABLE configs (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    config_type VARCHAR(64)  NOT NULL, -- rate_limit | log | timeout | ...
    version     VARCHAR(64)  NOT NULL,
    config_json JSON         NOT NULL,
    created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_type_version (config_type, version),
    KEY idx_type_created (config_type, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS configs;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS task_artifacts;
DROP TABLE IF EXISTS task_messages;
DROP TABLE IF EXISTS reliable_async_tasks;
DROP TABLE IF EXISTS friendships;
DROP TABLE IF EXISTS skills;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
