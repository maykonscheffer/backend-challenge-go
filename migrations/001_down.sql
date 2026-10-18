DROP TABLE outbox,ledger,transactions,wallets,schema_version CASCADE;
DROP FUNCTION immutable_row(),ledger_chain(),check_wallet_ledger();
