DO $$ BEGIN IF EXISTS(SELECT 1 FROM transactions WHERE status='PENDING_REFERENCE') THEN
 RAISE EXCEPTION 'resolve pending references before reverting migration 004'; END IF; END $$;
REVOKE UPDATE(status,result,reference_id,reference_next_attempt_at,reference_attempts) ON transactions FROM wallet_app;
DROP TRIGGER transaction_immutable ON transactions;
DROP FUNCTION guard_transaction_transition();
CREATE TRIGGER transaction_immutable BEFORE UPDATE OR DELETE ON transactions FOR EACH ROW EXECUTE FUNCTION immutable_row();
DROP INDEX pending_reference_due;
ALTER TABLE transactions DROP CONSTRAINT pending_input;
ALTER TABLE transactions DROP COLUMN input, DROP COLUMN reference_expires_at, DROP COLUMN reference_next_attempt_at, DROP COLUMN reference_attempts;
ALTER TABLE transactions DROP CONSTRAINT transactions_status_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_status_check CHECK(status IN ('PROCESSED','REJECTED'));
DELETE FROM schema_version WHERE version=4;
