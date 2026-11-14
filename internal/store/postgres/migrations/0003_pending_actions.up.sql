CREATE TABLE pending_actions (
    id            text PRIMARY KEY,
    user_id       bigint NOT NULL REFERENCES users (id),
    tool_call_id  bigint REFERENCES tool_calls (id),
    tool_name     text NOT NULL,
    -- json rather than jsonb: args_hash covers these exact bytes, and jsonb would reformat them.
    args_json     json NOT NULL,
    args_hash     text NOT NULL,
    preview       text NOT NULL,
    state         text NOT NULL CHECK (state IN ('proposed', 'approved', 'executed', 'failed',
                                                 'rejected', 'expired', 'superseded')),
    created_at    timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL,
    decided_at    timestamptz,
    executed_at   timestamptz,
    result_json   json,
    error         text NOT NULL DEFAULT '',
    superseded_by text REFERENCES pending_actions (id)
);

CREATE INDEX pending_actions_user_state ON pending_actions (user_id, state);
