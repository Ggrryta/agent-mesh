-- 添加服务发现相关字段到 capabilities 表
-- 执行时间: 2026-04-16
-- 说明: 支持 Nacos 服务动态发现

ALTER TABLE `capabilities`
ADD COLUMN `service_name` VARCHAR(128) DEFAULT '' COMMENT 'Nacos 服务名称（动态发现）' AFTER `endpoint`,
ADD COLUMN `group_name` VARCHAR(128) DEFAULT 'DEFAULT_GROUP' COMMENT 'Nacos 分组名称' AFTER `service_name`,
ADD INDEX `idx_service_name` (`service_name`);

-- 说明：
-- 1. service_name: 如果填写了服务名称，则使用 Nacos 服务发现动态获取实例地址
-- 2. group_name: Nacos 分组，默认为 DEFAULT_GROUP
-- 3. endpoint: 保留兼容旧版，当 service_name 为空时使用 endpoint 静态地址
--
-- 使用示例：
-- 旧版（静态地址）:
--   INSERT INTO capabilities (capability_id, endpoint, ...) 
--   VALUES ('my-skill', 'http://10.0.1.100:8080', ...);
--
-- 新版（动态发现）:
--   INSERT INTO capabilities (capability_id, service_name, group_name, ...) 
--   VALUES ('my-skill', 'my-skill-service', 'DEFAULT_GROUP', ...);
