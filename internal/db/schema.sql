-- Hyphae v0.1 SQLite schema (embedded).
-- Mirrors ~/.hyphae/spaces/m31labs-hyphae/protocols/schema.sql with the
-- additions required for v0.1 implementation (FTS5 virtual table).

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS spaces (
  id            TEXT PRIMARY KEY,
  uri           TEXT NOT NULL UNIQUE,
  authority     TEXT NOT NULL,
  scope         TEXT NOT NULL,
  visibility    TEXT NOT NULL,
  root_path     TEXT NOT NULL,
  trust_default TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  metadata_json TEXT
);

CREATE TABLE IF NOT EXISTS identities (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL,
  label         TEXT,
  metadata_json TEXT
);

CREATE TABLE IF NOT EXISTS capabilities (
  id                  TEXT PRIMARY KEY,
  subject_identity_id TEXT NOT NULL,
  space_id            TEXT NOT NULL,
  permissions_json    TEXT NOT NULL,
  limits_json         TEXT,
  issued_by           TEXT,
  issued_at           TEXT NOT NULL,
  expires_at          TEXT NOT NULL,
  revoked_at          TEXT
);

CREATE INDEX IF NOT EXISTS idx_capabilities_subject ON capabilities(subject_identity_id);
CREATE INDEX IF NOT EXISTS idx_capabilities_space   ON capabilities(space_id);

CREATE TABLE IF NOT EXISTS files (
  id               TEXT PRIMARY KEY,
  space_id         TEXT NOT NULL,
  path             TEXT NOT NULL,
  content_hash     TEXT NOT NULL,
  byte_size        INTEGER NOT NULL,
  token_count      INTEGER,
  parsed_at        TEXT NOT NULL,
  diagnostics_json TEXT,
  UNIQUE (space_id, path)
);

CREATE INDEX IF NOT EXISTS idx_files_space ON files(space_id);

CREATE TABLE IF NOT EXISTS objects (
  id            TEXT PRIMARY KEY,
  type          TEXT NOT NULL,
  space_id      TEXT NOT NULL,
  file_id       TEXT NOT NULL,
  status        TEXT,
  title         TEXT,
  tags_json     TEXT,
  summary       TEXT,
  metadata_json TEXT,
  updated_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_objects_type    ON objects(type);
CREATE INDEX IF NOT EXISTS idx_objects_space   ON objects(space_id);
CREATE INDEX IF NOT EXISTS idx_objects_updated ON objects(updated_at);

CREATE TABLE IF NOT EXISTS anchors (
  id           TEXT PRIMARY KEY,
  object_id    TEXT NOT NULL,
  heading_path TEXT,
  start_byte   INTEGER NOT NULL,
  end_byte     INTEGER NOT NULL,
  start_line   INTEGER NOT NULL,
  end_line     INTEGER NOT NULL,
  node_kind    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_anchors_object ON anchors(object_id);

CREATE TABLE IF NOT EXISTS edges (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL,
  src_id        TEXT NOT NULL,
  dst_id        TEXT NOT NULL,
  confidence    REAL,
  derivation    TEXT,
  agent_source  TEXT,
  created_by    TEXT,
  created_at    TEXT NOT NULL,
  metadata_json TEXT
);

CREATE INDEX IF NOT EXISTS idx_edges_src  ON edges(src_id);
CREATE INDEX IF NOT EXISTS idx_edges_dst  ON edges(dst_id);
CREATE INDEX IF NOT EXISTS idx_edges_kind ON edges(kind);

CREATE TABLE IF NOT EXISTS spores (
  id                TEXT PRIMARY KEY,
  space_id          TEXT NOT NULL,
  file_id           TEXT,
  status            TEXT NOT NULL,
  trust             TEXT NOT NULL,
  agent_identity_id TEXT,
  task_id           TEXT,
  run_id            TEXT,
  content_hash      TEXT NOT NULL,
  token_count       INTEGER,
  receipt_id        TEXT,
  submitted_at      TEXT NOT NULL,
  reviewed_at       TEXT,
  reviewed_by       TEXT,
  decision          TEXT,
  decision_reason   TEXT,
  metadata_json     TEXT
);

CREATE INDEX IF NOT EXISTS idx_spores_space  ON spores(space_id);
CREATE INDEX IF NOT EXISTS idx_spores_status ON spores(status);

CREATE TABLE IF NOT EXISTS proposed_writes (
  id           TEXT PRIMARY KEY,
  spore_id     TEXT NOT NULL,
  kind         TEXT NOT NULL,
  target       TEXT,
  payload_json TEXT NOT NULL,
  status       TEXT NOT NULL,
  applied_at   TEXT,
  applied_by   TEXT
);

CREATE INDEX IF NOT EXISTS idx_proposed_writes_spore ON proposed_writes(spore_id);

CREATE TABLE IF NOT EXISTS proposed_edges (
  id              TEXT PRIMARY KEY,
  spore_id        TEXT NOT NULL,
  src_id          TEXT NOT NULL,
  dst_id          TEXT NOT NULL,
  kind            TEXT NOT NULL,
  confidence      REAL NOT NULL,
  status          TEXT NOT NULL,
  applied_edge_id TEXT
);

CREATE INDEX IF NOT EXISTS idx_proposed_edges_spore ON proposed_edges(spore_id);

CREATE TABLE IF NOT EXISTS receipts (
  id            TEXT PRIMARY KEY,
  space_id      TEXT NOT NULL,
  subject_id    TEXT NOT NULL,
  subject_kind  TEXT NOT NULL,
  action        TEXT NOT NULL,
  status        TEXT NOT NULL,
  content_hash  TEXT,
  identity_id   TEXT,
  created_at    TEXT NOT NULL,
  expires_at    TEXT,
  metadata_json TEXT
);

CREATE INDEX IF NOT EXISTS idx_receipts_space   ON receipts(space_id);
CREATE INDEX IF NOT EXISTS idx_receipts_subject ON receipts(subject_id);

CREATE TABLE IF NOT EXISTS pulse_cache (
  id          TEXT PRIMARY KEY,
  space_id    TEXT NOT NULL,
  window      TEXT NOT NULL,
  body_json   TEXT NOT NULL,
  token_count INTEGER,
  computed_at TEXT NOT NULL,
  expires_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_pulse_space ON pulse_cache(space_id);

-- FTS5 virtual table over object summary + body, with content rowid linkage
-- to the objects table (external content table). Columns weighted at query
-- time: title^3, summary^2, body^1, tags^2.
CREATE VIRTUAL TABLE IF NOT EXISTS objects_fts USING fts5(
  id UNINDEXED,
  type UNINDEXED,
  space_id UNINDEXED,
  title,
  tags,
  summary,
  body,
  tokenize = 'porter unicode61'
);
