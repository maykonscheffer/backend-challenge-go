-- Transport context is separate from the immutable business event JSON.
ALTER TABLE outbox ADD COLUMN traceparent varchar(55) NOT NULL DEFAULT '';
ALTER TABLE outbox ADD COLUMN tracestate varchar(512) NOT NULL DEFAULT '';
INSERT INTO schema_version VALUES(9);
