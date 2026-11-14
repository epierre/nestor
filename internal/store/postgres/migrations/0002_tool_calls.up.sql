CREATE TABLE tool_calls (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         bigint NOT NULL,
    conversation_id bigint NOT NULL,
    model_call_id   bigint,
    tool_name       text NOT NULL,
    args_json       json NOT NULL,
    risk            text NOT NULL,
    status          text NOT NULL CHECK (status IN ('executed', 'rejected', 'error', 'pending', 'proposed')),
    result_json     json,
    error           text NOT NULL DEFAULT '',
    started_at      timestamptz NOT NULL,
    latency_ms      bigint NOT NULL DEFAULT 0,
    FOREIGN KEY (conversation_id, user_id) REFERENCES conversations (id, user_id)
);

CREATE INDEX tool_calls_user_started ON tool_calls (user_id, started_at);
