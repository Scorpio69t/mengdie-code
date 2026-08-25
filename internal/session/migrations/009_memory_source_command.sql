-- 009_memory_source_command.sql
-- 给 events 表加 source_command 列，让 ruleGoTest / ruleGoLint 在生产能触发。
-- 来源：Agent shell 工具在 ToolCompleted.Summary 之外把 ShellArgs 拼到 source_command JSON 字段。
ALTER TABLE events ADD COLUMN source_command TEXT;