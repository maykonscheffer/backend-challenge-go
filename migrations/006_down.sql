ALTER TABLE inbox DROP COLUMN completed_at;
DROP TRIGGER transaction_timestamps ON transactions;
DROP FUNCTION stamp_transaction_times();
ALTER TABLE transactions DROP CONSTRAINT transaction_times;
ALTER TABLE transactions DROP COLUMN updated_at, DROP COLUMN completed_at;
CREATE OR REPLACE FUNCTION guard_transaction_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'immutable audit record'; END IF;
 IF OLD.status<>'PENDING_REFERENCE' OR
 (to_jsonb(NEW)-ARRAY['status','result','reference_id','reference_next_attempt_at','reference_attempts']) IS DISTINCT FROM
 (to_jsonb(OLD)-ARRAY['status','result','reference_id','reference_next_attempt_at','reference_attempts']) THEN
 RAISE EXCEPTION 'invalid transaction transition'; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER wallet_updated ON wallets;
DROP FUNCTION stamp_wallet_update();
ALTER TABLE wallets DROP COLUMN updated_at;
DELETE FROM schema_version WHERE version=6;
