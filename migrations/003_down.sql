DROP INDEX outbox_pending_wallet;
REVOKE UPDATE(published_at,attempts,next_attempt_at) ON outbox FROM wallet_app;
ALTER TABLE outbox DROP COLUMN attempts, DROP COLUMN next_attempt_at;
DELETE FROM schema_version WHERE version=3;
