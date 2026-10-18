CREATE TABLE inbox (
 consumer text NOT NULL, message_id text NOT NULL, fingerprint text NOT NULL,
 deliveries integer NOT NULL DEFAULT 1 CHECK(deliveries>0),
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(consumer,message_id)
);
GRANT SELECT,INSERT,UPDATE ON inbox TO wallet_app;
INSERT INTO schema_version VALUES(2);
