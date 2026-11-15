CREATE TABLE tasks (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users (id),
    title      text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    -- A calendar day with no time zone: the owner's day, read in TIMEZONE when it is entered.
    due_date   date,
    created_at timestamptz NOT NULL DEFAULT now(),
    done_at    timestamptz
);

CREATE INDEX tasks_user_due ON tasks (user_id, due_date);

CREATE TABLE reminders (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users (id),
    text         text NOT NULL CHECK (char_length(text) BETWEEN 1 AND 500),
    due_at       timestamptz NOT NULL,
    -- The chat the reminder was created in; it is sent there and nowhere else.
    chat_id      bigint NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    cancelled_at timestamptz
);

CREATE INDEX reminders_user_due ON reminders (user_id, due_at);
CREATE INDEX reminders_pending_due ON reminders (due_at) WHERE delivered_at IS NULL AND cancelled_at IS NULL;
