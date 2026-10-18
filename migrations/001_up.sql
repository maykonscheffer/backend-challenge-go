CREATE TABLE schema_version (version integer PRIMARY KEY);
INSERT INTO schema_version VALUES (1);
CREATE TABLE wallets (
 id uuid PRIMARY KEY, player_id uuid NOT NULL, currency text NOT NULL CHECK(currency IN ('BRL','USD','EUR')),
 balance bigint NOT NULL CHECK(balance>=0), version bigint NOT NULL CHECK(version>=1),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(player_id,currency)
);
CREATE TABLE transactions (
 id uuid PRIMARY KEY, provider_id text, external_id text, idempotency_key text, fingerprint text,
 wallet_id uuid NOT NULL REFERENCES wallets(id), player_id uuid NOT NULL, currency text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
 amount bigint NOT NULL CHECK(amount>=0), round_id text, game_id text, reference_id uuid REFERENCES transactions(id),
 status text NOT NULL CHECK(status IN ('PROCESSED','REJECTED')),
 result jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(provider_id,external_id), UNIQUE(provider_id,idempotency_key),
 UNIQUE(id,wallet_id,currency,amount),
 CHECK ((kind='LOSS' AND amount=0) OR (kind<>'LOSS' AND amount>0)),
 CHECK ((kind='OPENING' AND provider_id IS NULL AND external_id IS NULL AND idempotency_key IS NULL AND fingerprint IS NULL AND round_id IS NULL AND game_id IS NULL AND status='PROCESSED') OR
 (kind<>'OPENING' AND provider_id IS NOT NULL AND external_id IS NOT NULL AND idempotency_key IS NOT NULL AND fingerprint IS NOT NULL AND round_id IS NOT NULL AND game_id IS NOT NULL)),
 CHECK (status<>'PROCESSED' OR kind NOT IN ('REFUND','ROLLBACK') OR reference_id IS NOT NULL)
);
CREATE UNIQUE INDEX one_opening ON transactions(wallet_id) WHERE kind='OPENING';
CREATE UNIQUE INDEX one_reversal ON transactions(reference_id) WHERE kind IN ('REFUND','ROLLBACK') AND status='PROCESSED';
CREATE TABLE ledger (
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 transaction_id uuid NOT NULL UNIQUE, wallet_id uuid NOT NULL REFERENCES wallets(id), currency text NOT NULL,
 amount bigint NOT NULL CHECK(amount>0), direction text NOT NULL CHECK(direction IN ('CREDIT','DEBIT')),
 before_balance bigint NOT NULL CHECK(before_balance>=0), after_balance bigint NOT NULL CHECK(after_balance>=0),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(transaction_id,wallet_id,currency,amount) REFERENCES transactions(id,wallet_id,currency,amount),
 CHECK (after_balance::numeric=before_balance::numeric+CASE WHEN direction='CREDIT' THEN amount::numeric ELSE -amount::numeric END)
);
CREATE INDEX ledger_wallet ON ledger(wallet_id,seq);
CREATE TABLE outbox (
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, event_id uuid NOT NULL UNIQUE,
 transaction_id uuid NOT NULL REFERENCES transactions(id), event_type text NOT NULL,
 payload jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz
);
CREATE FUNCTION immutable_row() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable audit record'; END $$;
CREATE TRIGGER ledger_immutable BEFORE UPDATE OR DELETE ON ledger FOR EACH ROW EXECUTE FUNCTION immutable_row();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON ledger FOR EACH STATEMENT EXECUTE FUNCTION immutable_row();
CREATE TRIGGER transaction_immutable BEFORE UPDATE OR DELETE ON transactions FOR EACH ROW EXECUTE FUNCTION immutable_row();
CREATE TRIGGER transaction_no_truncate BEFORE TRUNCATE ON transactions FOR EACH STATEMENT EXECUTE FUNCTION immutable_row();
CREATE FUNCTION ledger_chain() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE previous bigint; wallet_currency text;
BEGIN
 SELECT currency INTO wallet_currency FROM wallets WHERE id=NEW.wallet_id FOR UPDATE;
 SELECT after_balance INTO previous FROM ledger WHERE wallet_id=NEW.wallet_id ORDER BY seq DESC LIMIT 1;
 IF NEW.before_balance<>COALESCE(previous,0) OR NEW.currency<>wallet_currency THEN RAISE EXCEPTION 'invalid ledger chain'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ledger_chain_guard BEFORE INSERT ON ledger FOR EACH ROW EXECUTE FUNCTION ledger_chain();
CREATE FUNCTION check_wallet_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE wid uuid; stored bigint; calculated numeric;
BEGIN
 IF TG_TABLE_NAME='wallets' THEN wid:=NEW.id; ELSE wid:=NEW.wallet_id; END IF;
 SELECT balance INTO stored FROM wallets WHERE id=wid;
 SELECT COALESCE(SUM(CASE WHEN direction='CREDIT' THEN amount::numeric ELSE -amount::numeric END),0) INTO calculated FROM ledger WHERE wallet_id=wid;
 IF stored::numeric<>calculated THEN RAISE EXCEPTION 'wallet differs from ledger'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER wallet_consistent AFTER INSERT OR UPDATE ON wallets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_wallet_ledger();
CREATE CONSTRAINT TRIGGER ledger_consistent AFTER INSERT ON ledger DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_wallet_ledger();
GRANT SELECT ON schema_version TO wallet_app;
GRANT SELECT,INSERT,UPDATE ON wallets TO wallet_app;
GRANT SELECT,INSERT ON transactions,ledger,outbox TO wallet_app;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO wallet_app;
