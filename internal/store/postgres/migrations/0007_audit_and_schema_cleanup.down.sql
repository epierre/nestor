ALTER TABLE pending_actions DROP CONSTRAINT pending_actions_tool_call_user_fk;
ALTER TABLE pending_actions ADD CONSTRAINT pending_actions_tool_call_id_fkey
    FOREIGN KEY (tool_call_id) REFERENCES tool_calls (id);

ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_model_call_user_fk;

ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_id_user_unique;
ALTER TABLE model_calls DROP CONSTRAINT model_calls_id_user_unique;

ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_status_check;
ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_status_check
    CHECK (status IN ('executed', 'rejected', 'error', 'pending', 'proposed'));

ALTER TABLE user_identities ADD COLUMN chat_id bigint;
