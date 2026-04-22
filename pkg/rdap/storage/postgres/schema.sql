-- gordap canonical schema for the PostgreSQL DataSource provider.
--
-- Design rules:
--   - Everything RFC 9083 mandates, or that we index/query on, lives in its
--     own column. Don't bury those in JSONB.
--   - Multi-valued contact channels (emails, phones) live in join tables
--     so RFC 9536 reverse-search stays a plain indexed lookup.
--   - JSONB is reserved for genuinely open-ended data: secure_dns variants
--     and per-registrar `extras`.
--   - Every textual handle is a stable EPP ROID; the application never
--     synthesises handles.
--
-- Apply with: psql -1 -f schema.sql

CREATE EXTENSION IF NOT EXISTS citext;
-- pg_trgm gives index-assisted substring + infix matching for the
-- search endpoints. Without it, /domains?name=*foo* etc. fall back
-- to sequential scans, which is the throughput cap on serious loads.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- entities -------------------------------------------------------------
CREATE TABLE IF NOT EXISTS entities (
  handle         text PRIMARY KEY,
  kind           text NOT NULL CHECK (kind IN ('individual', 'org', 'organization')),
  full_name      text NOT NULL DEFAULT '',
  organization   text NOT NULL DEFAULT '',
  title          text NOT NULL DEFAULT '',
  country_code   char(2),
  locality       text NOT NULL DEFAULT '',
  region         text NOT NULL DEFAULT '',
  postal_code    text NOT NULL DEFAULT '',
  street         text[] NOT NULL DEFAULT '{}',
  created_at     timestamptz NOT NULL,
  updated_at     timestamptz NOT NULL,
  extras         jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS entities_country_idx ON entities (country_code);
-- jsonb_path_ops is smaller and faster than the default jsonb_ops for our
-- access patterns (containment only, no key-exists probes).
CREATE INDEX IF NOT EXISTS entities_extras_gin ON entities USING gin (extras jsonb_path_ops);

-- entity_emails --------------------------------------------------------
CREATE TABLE IF NOT EXISTS entity_emails (
  entity_handle text   NOT NULL REFERENCES entities(handle) ON DELETE CASCADE,
  email         citext NOT NULL,
  PRIMARY KEY (entity_handle, email)
);
CREATE INDEX IF NOT EXISTS entity_emails_email_idx ON entity_emails (email);

-- entity_phones --------------------------------------------------------
CREATE TABLE IF NOT EXISTS entity_phones (
  entity_handle text   NOT NULL REFERENCES entities(handle) ON DELETE CASCADE,
  number        text   NOT NULL,
  kinds         text[] NOT NULL DEFAULT '{}',
  PRIMARY KEY (entity_handle, number)
);

-- domains --------------------------------------------------------------
CREATE TABLE IF NOT EXISTS domains (
  handle            text PRIMARY KEY,
  ldh_name          text UNIQUE NOT NULL,
  unicode_name      text NOT NULL DEFAULT '',
  status            text[] NOT NULL DEFAULT '{}',
  registered_at     timestamptz NOT NULL,
  expires_at        timestamptz,
  last_changed      timestamptz NOT NULL,
  -- Per ICANN RP2.2 §2.3.1.3: timestamp at which the data in this row
  -- was last synchronised from the authoritative backend. Updated by
  -- ingest pipelines, not by the server.
  last_rdap_update  timestamptz NOT NULL DEFAULT now(),
  secure_dns        jsonb,
  registrar_handle  text REFERENCES entities(handle)
);
CREATE INDEX IF NOT EXISTS domains_registrar_idx ON domains (registrar_handle);
-- Prefix-LIKE acceleration: `LIKE 'foo%'` becomes a range scan on
-- this index. Required because the default B-tree on a UNIQUE column
-- uses text_ops, which only supports `=` for non-prefix matches.
CREATE INDEX IF NOT EXISTS domains_ldh_pattern ON domains (ldh_name text_pattern_ops);
-- Trigram GIN: handles `%foo%`, `%foo`, `foo%` substring and infix
-- searches via index lookup instead of seq-scan.
CREATE INDEX IF NOT EXISTS domains_ldh_trgm ON domains USING gin (ldh_name gin_trgm_ops);

-- domain_contacts ------------------------------------------------------
CREATE TABLE IF NOT EXISTS domain_contacts (
  domain_handle text NOT NULL REFERENCES domains(handle) ON DELETE CASCADE,
  entity_handle text NOT NULL REFERENCES entities(handle),
  role          text NOT NULL,
  PRIMARY KEY (domain_handle, entity_handle, role)
);
-- Reverse lookup: "all domains held by registrar X" or "all domains
-- where this contact appears". Without this index that query is a
-- seq-scan over the whole join table.
CREATE INDEX IF NOT EXISTS domain_contacts_entity_idx
  ON domain_contacts (entity_handle);

-- nameservers ----------------------------------------------------------
CREATE TABLE IF NOT EXISTS nameservers (
  handle        text PRIMARY KEY,
  ldh_name      text NOT NULL,
  unicode_name  text NOT NULL DEFAULT '',
  ipv4          inet[],
  ipv6          inet[]
);
CREATE UNIQUE INDEX IF NOT EXISTS nameservers_ldh_idx ON nameservers (lower(ldh_name));
-- Same prefix + trigram dance for nameserver search.
CREATE INDEX IF NOT EXISTS nameservers_ldh_pattern ON nameservers (ldh_name text_pattern_ops);
CREATE INDEX IF NOT EXISTS nameservers_ldh_trgm ON nameservers USING gin (ldh_name gin_trgm_ops);
-- Entity full_name + organization for /entities?fn=*. citext on email
-- already covers /entities?email=*; trigram on full_name lets fn=*
-- bypass the seq-scan.
CREATE INDEX IF NOT EXISTS entities_fullname_trgm ON entities USING gin (full_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS entities_org_trgm ON entities USING gin (organization gin_trgm_ops);
CREATE INDEX IF NOT EXISTS entity_emails_email_trgm ON entity_emails USING gin (email gin_trgm_ops);

CREATE TABLE IF NOT EXISTS domain_nameservers (
  domain_handle     text NOT NULL REFERENCES domains(handle) ON DELETE CASCADE,
  nameserver_handle text NOT NULL REFERENCES nameservers(handle),
  PRIMARY KEY (domain_handle, nameserver_handle)
);

-- ip_networks ----------------------------------------------------------
CREATE TABLE IF NOT EXISTS ip_networks (
  handle         text PRIMARY KEY,
  prefix         cidr NOT NULL,
  name           text NOT NULL DEFAULT '',
  type           text NOT NULL DEFAULT '',
  country        char(2),
  parent_handle  text,
  status         text[] NOT NULL DEFAULT '{}',
  registered_at  timestamptz NOT NULL,
  last_changed   timestamptz NOT NULL
);
-- GIST with inet_ops gives us O(log n) longest-prefix matches via `>>=`.
CREATE INDEX IF NOT EXISTS ip_networks_prefix_gist ON ip_networks USING gist (prefix inet_ops);
