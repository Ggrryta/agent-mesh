-- +goose Up
-- 加外键约束 + CASCADE DELETE，防止孤儿数据。
-- task 删除时自动级联删除关联的 messages / artifacts / inbox events。

ALTER TABLE task_messages
  ADD CONSTRAINT fk_task_messages_task
  FOREIGN KEY (task_id) REFERENCES reliable_async_tasks(task_id) ON DELETE CASCADE;

ALTER TABLE task_artifacts
  ADD CONSTRAINT fk_task_artifacts_task
  FOREIGN KEY (task_id) REFERENCES reliable_async_tasks(task_id) ON DELETE CASCADE;

ALTER TABLE inbox_events
  ADD CONSTRAINT fk_inbox_events_task
  FOREIGN KEY (task_id) REFERENCES reliable_async_tasks(task_id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE task_messages DROP FOREIGN KEY fk_task_messages_task;
ALTER TABLE task_artifacts DROP FOREIGN KEY fk_task_artifacts_task;
ALTER TABLE inbox_events DROP FOREIGN KEY fk_inbox_events_task;
