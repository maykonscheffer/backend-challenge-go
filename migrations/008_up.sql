ALTER TABLE ledger ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE ledger ADD CONSTRAINT ledger_id_unique UNIQUE(id);
INSERT INTO schema_version VALUES(8);
