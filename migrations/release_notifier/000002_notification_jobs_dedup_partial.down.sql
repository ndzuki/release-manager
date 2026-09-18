-- Restores the unconditional key. This FAILS if the database already holds two
-- jobs for one (operation_id, channel, recipient) -- the very state the forward
-- migration exists to allow -- so a rollback must reconcile those rows first.
DROP INDEX IF EXISTS idx_notification_jobs_dedup;

ALTER TABLE notification_jobs
    ADD CONSTRAINT notification_jobs_operation_id_channel_recipient_key
    UNIQUE (operation_id, channel, recipient);
