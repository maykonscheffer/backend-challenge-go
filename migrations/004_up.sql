ALTER TABLE transactions DROP CONSTRAINT transactions_status_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_status_check CHECK(status IN ('PROCESSED','REJECTED','PENDING_REFERENCE'));
ALTER TABLE transactions ADD COLUMN input jsonb,
 ADD COLUMN reference_expires_at timestamptz,
 ADD COLUMN reference_next_attempt_at timestamptz,
 ADD COLUMN reference_attempts integer NOT NULL DEFAULT 0 CHECK(reference_attempts>=0);
ALTER TABLE transactions ADD CONSTRAINT pending_input CHECK(status<>'PENDING_REFERENCE' OR
 (input IS NOT NULL AND reference_expires_at IS NOT NULL AND reference_next_attempt_at IS NOT NULL));
CREATE INDEX pending_reference_due ON transactions(reference_next_attempt_at) WHERE status='PENDING_REFERENCE';
DROP TRIGGER transaction_immutable ON transactions;
CREATE FUNCTION guard_transaction_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'immutable audit record'; END IF;
 IF OLD.status<>'PENDING_REFERENCE' OR
 (to_jsonb(NEW)-ARRAY['status','result','reference_id','reference_next_attempt_at','reference_attempts']) IS DISTINCT FROM
 (to_jsonb(OLD)-ARRAY['status','result','reference_id','reference_next_attempt_at','reference_attempts']) THEN
 RAISE EXCEPTION 'invalid transaction transition'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER transaction_immutable BEFORE UPDATE OR DELETE ON transactions FOR EACH ROW EXECUTE FUNCTION guard_transaction_transition();
GRANT UPDATE(status,result,reference_id,reference_next_attempt_at,reference_attempts) ON transactions TO wallet_app;
INSERT INTO schema_version VALUES(4);
