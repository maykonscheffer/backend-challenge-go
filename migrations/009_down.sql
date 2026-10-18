ALTER TABLE outbox DROP COLUMN tracestate, DROP COLUMN traceparent;
DELETE FROM schema_version WHERE version=9;
