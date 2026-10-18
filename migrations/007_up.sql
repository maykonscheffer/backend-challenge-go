ALTER TABLE transactions DROP CONSTRAINT transactions_status_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_status_check CHECK(status IN ('PROCESSED','REJECTED','PENDING_REFERENCE','FAILED'));
ALTER TABLE transactions DROP CONSTRAINT transaction_times;
ALTER TABLE transactions ADD CONSTRAINT transaction_times CHECK(updated_at>=created_at AND
 ((status='PENDING_REFERENCE' AND completed_at IS NULL) OR
 (status IN ('PROCESSED','REJECTED','FAILED') AND completed_at IS NOT NULL AND completed_at>=created_at)));
CREATE OR REPLACE FUNCTION stamp_transaction_times() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.updated_at:=clock_timestamp();
 NEW.completed_at:=CASE WHEN NEW.status IN ('PROCESSED','REJECTED','FAILED') THEN NEW.updated_at ELSE NULL END;
 RETURN NEW;
END $$;
INSERT INTO schema_version VALUES(7);
