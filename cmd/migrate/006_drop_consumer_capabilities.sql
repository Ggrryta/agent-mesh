-- 删除 Consumer 表上不再使用的 capability_ids 列。
-- 授权数据由 agent_permissions 表统一管理，该列是早期设计遗留，
-- 且允许 Consumer 自改权限，属于安全隐患。
ALTER TABLE consumers DROP COLUMN capability_ids;
