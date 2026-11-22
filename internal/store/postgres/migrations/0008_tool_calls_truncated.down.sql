UPDATE tool_calls SET status = 'executed' WHERE status = 'truncated';
ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_status_check;
ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_status_check
    CHECK (status IN ('executed', 'rejected', 'error', 'proposed'));
