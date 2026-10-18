DROP TRIGGER outbox_snapshot ON outbox;
DROP TRIGGER outbox_no_delete ON outbox;
DROP TRIGGER outbox_no_truncate ON outbox;
DROP FUNCTION guard_outbox_snapshot();
ALTER TABLE transactions DROP COLUMN correlation_id, DROP COLUMN causation_id;
DELETE FROM schema_version WHERE version=5;
