-- Run existing deferred consistency checks before further DDL in this transaction.
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE wallets ADD COLUMN updated_at timestamptz;
UPDATE wallets SET updated_at=created_at;
ALTER TABLE wallets ALTER COLUMN updated_at SET DEFAULT now(), ALTER COLUMN updated_at SET NOT NULL;
CREATE FUNCTION stamp_wallet_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.balance IS DISTINCT FROM OLD.balance OR NEW.version IS DISTINCT FROM OLD.version THEN
 NEW.updated_at:=clock_timestamp(); ELSE NEW.updated_at:=OLD.updated_at; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_updated BEFORE UPDATE ON wallets FOR EACH ROW EXECUTE FUNCTION stamp_wallet_update();

ALTER TABLE transactions ADD COLUMN updated_at timestamptz,
 ADD COLUMN completed_at timestamptz;
DROP TRIGGER transaction_immutable ON transactions;
UPDATE transactions SET updated_at=created_at,
 completed_at=CASE WHEN status IN ('PROCESSED','REJECTED') THEN created_at ELSE NULL END;
ALTER TABLE transactions ALTER COLUMN updated_at SET NOT NULL;
CREATE OR REPLACE FUNCTION guard_transaction_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'immutable audit record'; END IF;
 IF OLD.status<>'PENDING_REFERENCE' OR
 (to_jsonb(NEW)-ARRAY['status','result','reference_id','reference_next_attempt_at','reference_attempts','updated_at','completed_at']) IS DISTINCT FROM
 (to_jsonb(OLD)-ARRAY['status','result','reference_id','reference_next_attempt_at','reference_attempts','updated_at','completed_at']) THEN
 RAISE EXCEPTION 'invalid transaction transition'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER transaction_immutable BEFORE UPDATE OR DELETE ON transactions FOR EACH ROW EXECUTE FUNCTION guard_transaction_transition();
CREATE FUNCTION stamp_transaction_times() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.updated_at:=clock_timestamp();
 NEW.completed_at:=CASE WHEN NEW.status IN ('PROCESSED','REJECTED') THEN NEW.updated_at ELSE NULL END;
 RETURN NEW;
END $$;
CREATE TRIGGER transaction_timestamps BEFORE INSERT OR UPDATE ON transactions FOR EACH ROW EXECUTE FUNCTION stamp_transaction_times();
ALTER TABLE transactions ADD CONSTRAINT transaction_times CHECK(updated_at>=created_at AND
 ((status='PENDING_REFERENCE' AND completed_at IS NULL) OR (status IN ('PROCESSED','REJECTED') AND completed_at>=created_at)));
ALTER TABLE inbox ADD COLUMN completed_at timestamptz NOT NULL DEFAULT now();
UPDATE inbox SET completed_at=created_at;
COMMENT ON COLUMN inbox.completed_at IS 'First durable handling, including handoff to pending-reference worker; committed atomically with result.';
INSERT INTO schema_version VALUES(6);
SET CONSTRAINTS ALL DEFERRED;
