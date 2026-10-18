ALTER TABLE transactions ADD COLUMN correlation_id text NOT NULL DEFAULT '',
 ADD COLUMN causation_id text NOT NULL DEFAULT '';
CREATE FUNCTION guard_outbox_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(NEW)-ARRAY['published_at','attempts','next_attempt_at']) IS DISTINCT FROM
 (to_jsonb(OLD)-ARRAY['published_at','attempts','next_attempt_at']) THEN
 RAISE EXCEPTION 'immutable outbox snapshot'; END IF;
 IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
 RAISE EXCEPTION 'published event cannot return to pending'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER outbox_snapshot BEFORE UPDATE ON outbox FOR EACH ROW EXECUTE FUNCTION guard_outbox_snapshot();
CREATE TRIGGER outbox_no_delete BEFORE DELETE ON outbox FOR EACH ROW EXECUTE FUNCTION immutable_row();
CREATE TRIGGER outbox_no_truncate BEFORE TRUNCATE ON outbox FOR EACH STATEMENT EXECUTE FUNCTION immutable_row();
INSERT INTO schema_version VALUES(5);
