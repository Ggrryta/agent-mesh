-- 配置版本表：用于配置热更新的版本管理和审计追溯
-- 创建时间：2026-04-15

CREATE TABLE IF NOT EXISTS config_versions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键',
    version VARCHAR(64) NOT NULL COMMENT '版本号，格式 vYYYYMMDDNNN',
    config_type VARCHAR(32) NOT NULL COMMENT '配置类型：rate_limit / log',
    config_json JSON NOT NULL COMMENT '配置内容（JSON 格式）',
    change_summary VARCHAR(512) DEFAULT '' COMMENT '变更说明',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    created_by VARCHAR(128) DEFAULT '' COMMENT '操作人',
    
    UNIQUE KEY uk_version (version),
    INDEX idx_config_type (config_type),
    INDEX idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='配置版本表';
