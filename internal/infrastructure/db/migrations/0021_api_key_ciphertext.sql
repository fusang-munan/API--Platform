-- Store encrypted student API key material so owners can view the secret again.

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_ciphertext bytea;
