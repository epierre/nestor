ALTER TABLE pending_actions
    ADD COLUMN notify_chat_id bigint,
    ADD COLUMN outcome_notice_sent_at timestamptz;
