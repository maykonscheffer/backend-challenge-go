ALTER TABLE outbox ADD COLUMN attempts integer NOT NULL DEFAULT 0,
 ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX outbox_pending_wallet ON outbox ((payload->>'aggregateId'),seq) WHERE published_at IS NULL;
GRANT UPDATE(published_at,attempts,next_attempt_at) ON outbox TO wallet_app;
INSERT INTO schema_version VALUES(3);
