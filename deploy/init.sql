-- Local development credentials only. The application never uses the schema owner.
CREATE ROLE wallet_app LOGIN PASSWORD 'local-app-password' NOSUPERUSER NOCREATEDB NOCREATEROLE;
