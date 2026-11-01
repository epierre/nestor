CREATE TABLE users (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    display_name text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    disabled_at  timestamptz
);

CREATE TABLE user_identities (
    platform    text NOT NULL,
    external_id text NOT NULL,
    user_id     bigint NOT NULL REFERENCES users (id),
    chat_id     bigint,
    disabled_at timestamptz,
    PRIMARY KEY (platform, external_id)
);

CREATE INDEX user_identities_user_id ON user_identities (user_id);

CREATE TABLE conversations (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users (id),
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz,
    -- Target of the composite foreign keys that keep child rows in their conversation's user.
    UNIQUE (id, user_id)
);

CREATE UNIQUE INDEX conversations_one_open_per_user ON conversations (user_id) WHERE ended_at IS NULL;

CREATE TABLE messages (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         bigint NOT NULL,
    conversation_id bigint NOT NULL,
    role            text NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    content         text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (conversation_id, user_id) REFERENCES conversations (id, user_id)
);

CREATE INDEX messages_recent ON messages (user_id, conversation_id, id DESC);

CREATE TABLE model_calls (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           bigint NOT NULL,
    conversation_id   bigint NOT NULL,
    model             text NOT NULL,
    started_at        timestamptz NOT NULL,
    latency_ms        bigint NOT NULL,
    prompt_tokens     integer NOT NULL DEFAULT 0,
    completion_tokens integer NOT NULL DEFAULT 0,
    cost_usd          numeric, -- null, not zero, when the proxy reports no cost
    status            text NOT NULL CHECK (status IN ('ok', 'error')),
    error             text NOT NULL DEFAULT '',
    -- json rather than jsonb: bodies are kept as received, and jsonb refuses \u0000 in strings.
    request_json      json NOT NULL,
    response_json     json,
    FOREIGN KEY (conversation_id, user_id) REFERENCES conversations (id, user_id)
);

CREATE INDEX model_calls_user_started ON model_calls (user_id, started_at);
