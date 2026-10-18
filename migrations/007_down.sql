DO $$ BEGIN IF EXISTS(SELECT 1 FROM transactions WHERE status='FAILED') THEN
 RAISE EXCEPTION 'FAILED records cannot be represented by migration 006'; END IF; END $$;
ALTER TABLE transactions DROP CONSTRAINT transactions_status_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_status_check CHECK(status IN ('PROCESSED','REJECTED','PENDING_REFERENCE'));
ALTER TABLE transactions DROP CONSTRAINT transaction_times;
ALTER TABLE transactions ADD CONSTRAINT transaction_times CHECK(updated_at>=created_at AND
 ((status='PENDING_REFERENCE' AND completed_at IS NULL) OR (status IN ('PROCESSED','REJECTED') AND completed_at>=created_at)));
CREATE OR REPLACE FUNCTION stamp_transaction_times() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.updated_at:=clock_timestamp();
 NEW.completed_at:=CASE WHEN NEW.status IN ('PROCESSED','REJECTED') THEN NEW.updated_at ELSE NULL END;
 RETURN NEW;
END $$;
DELETE FROM schema_version WHERE version=7;
