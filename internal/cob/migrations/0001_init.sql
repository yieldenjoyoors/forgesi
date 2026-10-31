-- ForgeSI knot storage: git objects, COB refs, and the repo registry.
-- Objects are raw git objects (header included); oids are their sha1 hex.
CREATE TABLE IF NOT EXISTS knot_objects (
  oid     TEXT PRIMARY KEY,
  content BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS knot_refs (
  ref     TEXT PRIMARY KEY,
  oid     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knot_repos (
  did            TEXT PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  default_branch TEXT NOT NULL DEFAULT 'main',
  created_at     TEXT NOT NULL
);
