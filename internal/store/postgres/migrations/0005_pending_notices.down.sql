ALTER TABLE pending_actions
    DROP COLUMN IF EXISTS outcome_notice_sent_at,
    DROP COLUMN IF EXISTS notify_chat_id;
