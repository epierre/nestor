-- Per-user secrets, such as the token an MCP server needs for one user. Values are encrypted in
-- the application with AES-GCM under CREDENTIALS_KEY; the database never holds plaintext.
CREATE TABLE user_credentials (
    user_id    bigint NOT NULL REFERENCES users (id),
    key        text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 100),
    ciphertext bytea NOT NULL,
    nonce      bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
