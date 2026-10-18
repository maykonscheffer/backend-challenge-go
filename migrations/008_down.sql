ALTER TABLE ledger DROP CONSTRAINT ledger_id_unique;
ALTER TABLE ledger DROP COLUMN id;
DELETE FROM schema_version WHERE version=8;
