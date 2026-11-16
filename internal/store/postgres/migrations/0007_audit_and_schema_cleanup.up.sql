ALTER TABLE user_identities DROP COLUMN chat_id;

UPDATE tool_calls SET status = 'proposed' WHERE status = 'pending';
ALTER TABLE tool_calls DROP CONSTRAINT tool_calls_status_check;
ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_status_check
    CHECK (status IN ('executed', 'rejected', 'error', 'proposed'));

ALTER TABLE model_calls ADD CONSTRAINT model_calls_id_user_unique UNIQUE (id, user_id);
ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_id_user_unique UNIQUE (id, user_id);

ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_model_call_user_fk
    FOREIGN KEY (model_call_id, user_id) REFERENCES model_calls (id, user_id);

ALTER TABLE pending_actions DROP CONSTRAINT pending_actions_tool_call_id_fkey;
ALTER TABLE pending_actions ADD CONSTRAINT pending_actions_tool_call_user_fk
    FOREIGN KEY (tool_call_id, user_id) REFERENCES tool_calls (id, user_id);
